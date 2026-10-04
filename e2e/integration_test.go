// Package e2e — интеграционные тесты полного контура: state-manager (Postgres + Redis),
// Redis Streams и тестовый оператор (fixtures/e2e-operator). Запуск: make e2e или ./scripts/e2e.sh.
//
// Тесты этого файла не зависят от режима state-manager: scripts/e2e.sh прогоняет их и без
// авторизации, и с авторизацией (E2E_AUTH=1) — тогда клиент и оператор ходят с JWT.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// skipIfNoEnv пропускает тесты без поднятого стенда (локальный go test без Docker).
func skipIfNoEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_STORAGE_URL") == "" {
		t.Skip("set E2E_STORAGE_URL (run ./scripts/e2e.sh)")
	}
}

// setupE2E возвращает shard (по умолчанию e2e-shard-1) и клиент state-manager.
func setupE2E(t *testing.T) (shard string, cl *SMClient) {
	t.Helper()
	skipIfNoEnv(t)
	base := os.Getenv("E2E_STORAGE_URL")
	shard = os.Getenv("E2E_SHARD_ID")
	if shard == "" {
		shard = "e2e-shard-1"
	}
	var err error
	cl, err = NewSMClient(base)
	require.NoError(t, err)
	if authEnabled() {
		// Права клиента — из БД по sub (fixtures/auth/seed.sql).
		cl = cl.WithToken(signToken(t, tokenOpts{Subject: authClientSubject}))
	}
	return shard, cl
}

// startTestOperator запускает бинарник e2e-operator с типичным окружением и регистрирует
// принудительную остановку в cleanup. Если logLevel непустой — выставляется E2E_LOG_LEVEL.
func startTestOperator(t *testing.T, shard string, logLevel string) {
	t.Helper()
	bin := os.Getenv("E2E_OPERATOR_BIN")
	require.NotEmpty(t, bin, "E2E_OPERATOR_BIN")
	base := os.Getenv("E2E_STORAGE_URL")
	cmd := exec.Command(bin)
	env := append(os.Environ(),
		"E2E_SHARD_ID="+shard,
		"E2E_STORAGE_URL="+base,
		"E2E_INFORMER_URL="+os.Getenv("E2E_INFORMER_URL"),
	)
	if logLevel != "" {
		env = append(env, "E2E_LOG_LEVEL="+logLevel)
	}
	if authEnabled() {
		// Оператор получает права в claim токена, ограниченные своим шардом.
		env = append(env, "E2E_OPERATOR_TOKEN="+operatorToken(t, shard))
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { stopOperatorProcess(cmd) })
}

// TestE2E_CreateBecomesReady проверяет «счастливый путь» создания ресурса:
// POST в state-manager → событие в Redis → информер → reconcile → финализатор и
// доведение до Ready. Ожидаем условие Ready=True в status и совпадение version с current_version
// после синхронизации с хранилищем.
func TestE2E_CreateBecomesReady(t *testing.T) {
	shard, cl := setupE2E(t)
	name := "widget-ready-" + time.Now().Format("150405")
	t.Log("create", name)
	_, err := cl.CreateResource(shard, name, json.RawMessage(`{}`), nil)
	require.NoError(t, err)

	startTestOperator(t, shard, "4")

	err = waitUntil(30*time.Second, 400*time.Millisecond, func() (bool, error) {
		r, code, err := cl.GetResource(name)
		if err != nil || code != 200 {
			return false, err
		}
		return readyTrueFromStatus(r.Status)
	})
	require.NoError(t, err)

	r, _, err := cl.GetResource(name)
	require.NoError(t, err)
	var st statusShape
	require.NoError(t, json.Unmarshal(r.Status, &st))
	require.Equal(t, r.Version, r.CurrentVersion, "version should match after sync")
}

// TestE2E_RequeueExtraThenReady проверяет несколько проходов reconcile с Requeue:
// в spec задано requeueExtra=2 — reconciler увеличивает reconcilePasses и возвращает Requeue,
// пока не выполнит нужное число дополнительных проходов, затем выставляет Ready.
// Успех: Ready=True и reconcilePasses >= 2.
func TestE2E_RequeueExtraThenReady(t *testing.T) {
	shard, cl := setupE2E(t)
	name := "widget-req-" + time.Now().Format("150405")
	spec := json.RawMessage(`{"requeueExtra":2}`)
	_, err := cl.CreateResource(shard, name, spec, nil)
	require.NoError(t, err)

	startTestOperator(t, shard, "")

	err = waitUntil(30*time.Second, 400*time.Millisecond, func() (bool, error) {
		r, code, err := cl.GetResource(name)
		if err != nil || code != 200 {
			return false, err
		}
		var st statusShape
		if err := json.Unmarshal(r.Status, &st); err != nil {
			return false, err
		}
		ok, err := readyTrueFromStatus(r.Status)
		if !ok || err != nil {
			return false, err
		}
		return st.ReconcilePasses >= 2, nil
	})
	require.NoError(t, err)
}

// TestE2E_DeleteWithFinalizer проверяет удаление при наличии финализатора (operator добавляет его):
// 1) ресурс доходит до Ready; 2) клиент вызывает DELETE — state-manager выставляет deletion_timestamp;
// 3) reconcile delete снимает finalizers через UpdateStatus; 4) при пустых finalizers state-manager
// удаляет строку — GET возвращает 404 (второй HTTP DELETE не вызывается).
func TestE2E_DeleteWithFinalizer(t *testing.T) {
	shard, cl := setupE2E(t)
	name := "widget-del-" + time.Now().Format("150405")
	_, err := cl.CreateResource(shard, name, json.RawMessage(`{}`), nil)
	require.NoError(t, err)

	startTestOperator(t, shard, "")

	require.NoError(t, waitUntil(30*time.Second, 400*time.Millisecond, func() (bool, error) {
		r, code, err := cl.GetResource(name)
		if err != nil || code != 200 {
			return false, err
		}
		return readyTrueFromStatus(r.Status)
	}))

	require.NoError(t, cl.DeleteResource(name))

	require.NoError(t, waitUntil(30*time.Second, 400*time.Millisecond, func() (bool, error) {
		_, code, err := cl.GetResource(name)
		if err != nil {
			return false, err
		}
		return code == 404, nil
	}))
}

// TestE2E_LabelsListInReconcile проверяет лейблы end-to-end: несколько ресурсов с разными labels,
// ресурс-агрегатор с spec.labelTestQueries — в reconcile вызывается Storage.List с label selectors
// (как в state-manager HTTP). В status записываются результаты; дополнительно проверяется HTTP List.
func TestE2E_LabelsListInReconcile(t *testing.T) {
	shard, cl := setupE2E(t)
	sfx := time.Now().Format("20060102150405")
	n1 := "lbl-a1-" + sfx
	n2 := "lbl-a2-" + sfx
	n3 := "lbl-b1-" + sfx
	nq := "lbl-q-" + sfx

	_, err := cl.CreateResource(shard, n1, json.RawMessage(`{}`), map[string]string{"team": "alpha", "env": "shared"})
	require.NoError(t, err)
	_, err = cl.CreateResource(shard, n2, json.RawMessage(`{}`), map[string]string{"team": "alpha", "env": "prod"})
	require.NoError(t, err)
	_, err = cl.CreateResource(shard, n3, json.RawMessage(`{}`), map[string]string{"team": "beta", "env": "shared"})
	require.NoError(t, err)

	spec := json.RawMessage(`{"labelTestQueries":["team in (alpha)","env in (shared)","team in (alpha),env in (shared)"]}`)
	_, err = cl.CreateResource(shard, nq, spec, map[string]string{"team": "query", "env": "coord"})
	require.NoError(t, err)

	startTestOperator(t, shard, "")

	require.NoError(t, waitUntil(30*time.Second, 400*time.Millisecond, func() (bool, error) {
		r, code, err := cl.GetResource(nq)
		if err != nil || code != 200 {
			return false, err
		}
		if !readyTrueFromStatusSimple(r.Status) {
			return false, nil
		}
		var st struct {
			LabelQueryResults []struct {
				Selector string   `json:"selector"`
				Count    int      `json:"count"`
				Names    []string `json:"names"`
			} `json:"labelQueryResults"`
		}
		if err := json.Unmarshal(r.Status, &st); err != nil {
			return false, err
		}
		return len(st.LabelQueryResults) == 3, nil
	}))

	r, _, err := cl.GetResource(nq)
	require.NoError(t, err)
	var st struct {
		LabelQueryResults []struct {
			Selector string   `json:"selector"`
			Count    int      `json:"count"`
			Names    []string `json:"names"`
		} `json:"labelQueryResults"`
	}
	require.NoError(t, json.Unmarshal(r.Status, &st))
	require.Len(t, st.LabelQueryResults, 3)

	countOf := func(sel string) int {
		for _, q := range st.LabelQueryResults {
			if q.Selector == sel {
				return q.Count
			}
		}
		return -1
	}
	require.Equal(t, 2, countOf("team in (alpha)"), "team=alpha: a1+a2")
	require.Equal(t, 2, countOf("env in (shared)"), "env=shared: a1+b1")
	require.Equal(t, 1, countOf("team in (alpha),env in (shared)"), "intersection: a1")

	listAlpha, err := cl.ListResources(shard, "team in (alpha)")
	require.NoError(t, err)
	require.Len(t, listAlpha, 2)
}

func readyTrueFromStatusSimple(statusJSON json.RawMessage) bool {
	ok, err := readyTrueFromStatus(statusJSON)
	return err == nil && ok
}
