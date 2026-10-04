package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Таймауты ожиданий по умолчанию.
const (
	readyTimeout      = 30 * time.Second
	pollStep          = 200 * time.Millisecond
	operatorReadyWait = 30 * time.Second
	// operatorStopTimeout — сколько ждать корректного завершения после SIGTERM, затем SIGKILL.
	operatorStopTimeout = 30 * time.Second
	cleanupStopTimeout  = 10 * time.Second
)

// randHex — случайный суффикс для имён и шардов: тесты не пересекаются между собой и прогонами.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uniqueName(prefix string) string {
	return prefix + "-" + randHex(4)
}

// newShard — отдельный шард на тест: свой Redis-стрим, свой ListPending, нет влияния
// «хвостов» от других тестов и можно запускать тесты параллельно.
func newShard() string {
	return "e2e-" + randHex(4)
}

// knownBug пропускает тест, фиксирующий известный баг библиотек, пока не задан E2E_KNOWN_BUGS.
// Тест описывает ожидаемое (правильное) поведение и падает, пока баг не исправлен.
func knownBug(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("E2E_KNOWN_BUGS") == "" {
		t.Skipf("known bug: %s (set E2E_KNOWN_BUGS=1 to run)", reason)
	}
}

// onceAcrossPhases — тест не зависит от режима авторизации и тяжёлый: гоняем его только в фазе no-auth.
func onceAcrossPhases(t *testing.T) {
	t.Helper()
	if authEnabled() {
		t.Skip("mode-independent test, runs in the no-auth phase only")
	}
}

// ---------------------------------------------------------------------------
// Оператор
// ---------------------------------------------------------------------------

// operatorOpts — параметры запуска e2e-operator.
type operatorOpts struct {
	Shard    string
	LogLevel string
	Workers  int
	// Token: nil — по умолчанию (в фазе auth токен шарда), указатель на "" — без токена.
	Token *string
	// StorageURL / InformerURL переопределяют адреса стенда (пустые — из окружения).
	StorageURL  string
	InformerURL string
	// Unset — переменные, которые нужно убрать из окружения процесса.
	Unset []string
	// NoWaitReady — не ждать готовности (для сценариев, где оператор должен упасть на старте).
	NoWaitReady bool
}

// operatorProc — запущенный процесс оператора. Логи копятся в буфер и выводятся, если тест упал
// (E2E_OPERATOR_LOGS=1 — дублировать их в stdout сразу).
type operatorProc struct {
	t         *testing.T
	cmd       *exec.Cmd
	logs      *syncBuffer
	readyFile string
	done      chan struct{}
	waitErr   error
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func strPtr(s string) *string { return &s }

func startOperator(t *testing.T, o operatorOpts) *operatorProc {
	t.Helper()
	bin := os.Getenv("E2E_OPERATOR_BIN")
	require.NotEmpty(t, bin, "E2E_OPERATOR_BIN")
	if o.Shard == "" {
		o.Shard = newShard()
	}
	storage := o.StorageURL
	if storage == "" {
		storage = os.Getenv("E2E_STORAGE_URL")
	}
	informer := o.InformerURL
	if informer == "" {
		informer = os.Getenv("E2E_INFORMER_URL")
	}
	p := &operatorProc{
		t:         t,
		logs:      &syncBuffer{},
		readyFile: filepath.Join(t.TempDir(), "operator-ready"),
		done:      make(chan struct{}),
	}

	env := filterEnv(os.Environ(), append([]string{
		"E2E_SHARD_ID", "E2E_STORAGE_URL", "E2E_INFORMER_URL", "E2E_LOG_LEVEL",
		"E2E_OPERATOR_TOKEN", "E2E_WORKERS", "E2E_READY_FILE",
	}, o.Unset...))
	if !contains(o.Unset, "E2E_SHARD_ID") {
		env = append(env, "E2E_SHARD_ID="+o.Shard)
	}
	if !contains(o.Unset, "E2E_STORAGE_URL") {
		env = append(env, "E2E_STORAGE_URL="+storage)
	}
	if !contains(o.Unset, "E2E_INFORMER_URL") {
		env = append(env, "E2E_INFORMER_URL="+informer)
	}
	env = append(env, "E2E_READY_FILE="+p.readyFile)
	if o.LogLevel != "" {
		env = append(env, "E2E_LOG_LEVEL="+o.LogLevel)
	}
	if o.Workers > 0 {
		env = append(env, fmt.Sprintf("E2E_WORKERS=%d", o.Workers))
	}
	switch {
	case o.Token != nil:
		if *o.Token != "" {
			env = append(env, "E2E_OPERATOR_TOKEN="+*o.Token)
		}
	case authEnabled():
		// Оператор получает права в claim токена, ограниченные своим шардом.
		env = append(env, "E2E_OPERATOR_TOKEN="+operatorToken(t, o.Shard))
	}

	cmd := exec.Command(bin)
	cmd.Env = env
	var out io.Writer = p.logs
	if os.Getenv("E2E_OPERATOR_LOGS") != "" {
		out = io.MultiWriter(p.logs, os.Stdout)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(t, cmd.Start())
	p.cmd = cmd
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()

	t.Cleanup(func() {
		// Короче operatorStopTimeout: graceful stop проверяют отдельные тесты, а здесь важно
		// не ждать зависший Stop (известный баг с элементами в RequeueAfter / backoff).
		if _, graceful := p.Stop(cleanupStopTimeout); !graceful {
			// Известный баг controlloop: Stop не возвращается, пока в очереди элемент в
			// RequeueAfter / backoff. Видно всегда, а в режиме E2E_KNOWN_BUGS тест падает.
			msg := fmt.Sprintf("known bug: operator (shard %s) did not stop on SIGTERM in %s and was killed", o.Shard, cleanupStopTimeout)
			if os.Getenv("E2E_KNOWN_BUGS") != "" {
				t.Error(msg)
			} else {
				t.Log(msg)
			}
		}
		if t.Failed() {
			t.Logf("operator (shard %s) logs:\n%s", o.Shard, p.logs.String())
		}
	})

	if !o.NoWaitReady {
		p.WaitReady()
	}
	return p
}

// WaitReady ждёт, пока оператор выполнит mgr.Run: информер подписан, ListPending отработал.
// После этого события о новых ресурсах уже не теряются.
func (p *operatorProc) WaitReady() {
	p.t.Helper()
	deadline := time.Now().Add(operatorReadyWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p.readyFile); err == nil {
			return
		}
		if p.Exited() {
			p.t.Fatalf("operator exited before ready: %v\nlogs:\n%s", p.waitErr, p.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("operator is not ready after %s\nlogs:\n%s", operatorReadyWait, p.logs.String())
}

func (p *operatorProc) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// WaitExit ждёт завершения процесса; ok=false — не завершился за timeout.
func (p *operatorProc) WaitExit(timeout time.Duration) (exitCode int, ok bool) {
	select {
	case <-p.done:
		return p.exitCode(), true
	case <-time.After(timeout):
		return 0, false
	}
}

func (p *operatorProc) exitCode() int {
	if p.waitErr == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(p.waitErr, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Stop посылает SIGTERM и ждёт graceful-завершения не дольше timeout, затем SIGKILL.
// graceful=false — процесс пришлось убить.
func (p *operatorProc) Stop(timeout time.Duration) (exitCode int, graceful bool) {
	if p.Exited() {
		return p.exitCode(), true
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if code, ok := p.WaitExit(timeout); ok {
		return code, true
	}
	_ = p.cmd.Process.Kill()
	<-p.done
	return p.exitCode(), false
}

func (p *operatorProc) Logs() string { return p.logs.String() }

func filterEnv(env []string, drop []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !contains(drop, k) {
			out = append(out, kv)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Статус виджета и ожидания
// ---------------------------------------------------------------------------

// widgetStatus — status виджета, который пишет fixtures/e2e-operator.
type widgetStatus struct {
	Conditions []struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"conditions"`
	ReconcilePasses    int         `json:"reconcilePasses"`
	ObservedGeneration int64       `json:"observedGeneration"`
	Reconciles         int         `json:"reconciles"`
	Failures           int         `json:"failures"`
	Panics             int         `json:"panics"`
	RequeueAfters      int         `json:"requeueAfters"`
	ObservedMarker     string      `json:"observedMarker"`
	PassTimes          []time.Time `json:"passTimes"`
	MaxParallel        int         `json:"maxParallel"`
	SameKeyOverlap     bool        `json:"sameKeyOverlap"`
	ChildrenReady      []string    `json:"childrenReady"`
	RemoteNotes        []string    `json:"remoteNotes"`
	LabelQueryResults  []struct {
		Selector string   `json:"selector"`
		Count    int      `json:"count"`
		Names    []string `json:"names"`
	} `json:"labelQueryResults"`
}

func parseWidgetStatus(raw json.RawMessage) (widgetStatus, error) {
	var st widgetStatus
	if len(raw) == 0 || string(raw) == "null" {
		return st, nil
	}
	err := json.Unmarshal(raw, &st)
	return st, err
}

func (s widgetStatus) cond(t string) string {
	for _, c := range s.Conditions {
		if c.Type == t {
			return c.Status
		}
	}
	return ""
}

func (s widgetStatus) ready() bool { return s.cond("Ready") == "True" }

// synced — оператор завершил проход по текущей spec: Ready, Synced и version == current_version.
func synced(r *ResourceDTO, st widgetStatus) bool {
	return st.ready() && st.cond("E2EWidgetSynced") == "True" && r.Version == r.CurrentVersion
}

// waitResource ждёт, пока pred вернёт true для ресурса kind/name; при таймауте тест падает
// с последним увиденным состоянием.
func waitResource(t *testing.T, cl *SMClient, kind, name string, timeout time.Duration,
	pred func(r *ResourceDTO, st widgetStatus) bool) *ResourceDTO {
	t.Helper()
	var last string
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, code, err := cl.GetKind(kind, name)
		switch {
		case err != nil:
			last = err.Error()
		case code != 200:
			last = fmt.Sprintf("HTTP %d", code)
		default:
			st, err := parseWidgetStatus(r.Status)
			if err != nil {
				last = err.Error()
				break
			}
			if pred(r, st) {
				return r
			}
			last = fmt.Sprintf("version=%d current_version=%d finalizers=%v deletion=%v status=%s",
				r.Version, r.CurrentVersion, r.Finalizers, r.DeletionTimestamp, string(r.Status))
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("%s/%s: condition not met in %s; last state: %s", kind, name, timeout, last)
	return nil
}

func waitWidget(t *testing.T, cl *SMClient, name string, pred func(r *ResourceDTO, st widgetStatus) bool) *ResourceDTO {
	t.Helper()
	return waitResource(t, cl, widgetKind, name, readyTimeout, pred)
}

// waitSynced ждёт полной обработки виджета оператором и возвращает его статус.
func waitSynced(t *testing.T, cl *SMClient, name string) (*ResourceDTO, widgetStatus) {
	t.Helper()
	r := waitWidget(t, cl, name, synced)
	st, err := parseWidgetStatus(r.Status)
	require.NoError(t, err)
	return r, st
}

// waitGone ждёт 404 по виджету.
func waitGone(t *testing.T, cl *SMClient, name string) {
	t.Helper()
	require.NoError(t, waitUntil(readyTimeout, pollStep, func() (bool, error) {
		_, code, err := cl.GetResource(name)
		return code == 404, err
	}), "%s must be deleted", name)
}

// requireNever проверяет, что cond не становится true в течение d.
func requireNever(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		require.False(t, cond(), msg)
		time.Sleep(pollStep)
	}
}

func getWidget(t *testing.T, cl *SMClient, name string) (*ResourceDTO, widgetStatus) {
	t.Helper()
	r, code, err := cl.GetResource(name)
	require.NoError(t, err)
	require.Equal(t, 200, code, "get %s", name)
	st, err := parseWidgetStatus(r.Status)
	require.NoError(t, err)
	return r, st
}

// hasStatus — оператор уже записал status.
func hasStatus(r *ResourceDTO) bool {
	s := strings.TrimSpace(string(r.Status))
	return s != "" && s != "null" && s != "{}"
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func createWidget(t *testing.T, cl *SMClient, shard, name string, spec any) *ResourceDTO {
	t.Helper()
	if spec == nil {
		spec = map[string]any{}
	}
	r, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Spec: mustJSON(spec)})
	require.NoError(t, err)
	return r
}

// ---------------------------------------------------------------------------
// Стенд: docker compose и psql
// ---------------------------------------------------------------------------

// compose выполняет docker compose с файлами текущей фазы (E2E_COMPOSE_FILES, через ':').
func compose(t *testing.T, args ...string) string {
	t.Helper()
	out, err := composeE(args...)
	require.NoError(t, err, "docker compose %v: %s", args, out)
	return out
}

func composeE(args ...string) (string, error) {
	return composeCtx(context.Background(), args...)
}

func composeCtx(ctx context.Context, args ...string) (string, error) {
	root := os.Getenv("E2E_ROOT")
	files := os.Getenv("E2E_COMPOSE_FILES")
	if root == "" || files == "" {
		return "", fmt.Errorf("E2E_ROOT / E2E_COMPOSE_FILES are not set")
	}
	var full []string
	for _, f := range strings.Split(files, ":") {
		full = append(full, "-f", f)
	}
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, full...), args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func skipIfNoCompose(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_COMPOSE_FILES") == "" {
		t.Skip("E2E_COMPOSE_FILES is not set (run ./scripts/e2e.sh)")
	}
}

// psql выполняет SQL в базе state-manager и возвращает вывод без форматирования.
func psql(t *testing.T, sql string) string {
	t.Helper()
	return strings.TrimSpace(compose(t, "exec", "-T", "postgres",
		"psql", "-U", "e2e", "-d", "e2e", "-v", "ON_ERROR_STOP=1", "-tA", "-c", sql))
}

// waitStateManager ждёт /health/live после рестарта контейнера.
func waitStateManager(t *testing.T, cl *SMClient) {
	t.Helper()
	require.NoError(t, waitUntil(60*time.Second, 500*time.Millisecond, func() (bool, error) {
		code, _, err := cl.Do("GET", "/health/live", nil)
		return err == nil && code == 200, nil
	}), "state-manager is not live")
}
