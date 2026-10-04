package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// Дополнительные проверки авторизации state-manager (фаза auth). Права клиента-админа
// (setupE2E) — из БД; остальные токены собираются в тестах.

// widgetRule — правило на виджеты в namespace default (поля пустые — любые значения).
func widgetRule(shard string, verbs ...string) map[string]any {
	r := map[string]any{"verbs": verbs, "resource_group": widgetGroup, "namespace": testNamespace, "kind": widgetKind}
	if shard != "" {
		r["shard_id"] = shard
	}
	return r
}

func claimClient(t *testing.T, subject string, rules ...map[string]any) *SMClient {
	t.Helper()
	if rules == nil {
		rules = []map[string]any{}
	}
	return rawClient(t).WithToken(signToken(t, tokenOpts{Subject: subject, Permissions: rules}))
}

// widgetListPath — list виджетов, покрытый правилом на group+namespace+kind (+ доп. фильтры).
func widgetListPath(kv ...string) string {
	f := map[string]string{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace}
	for i := 0; i < len(kv); i += 2 {
		f[kv[i]] = kv[i+1]
	}
	return listPath(f)
}

// rawAuthRequest — запрос с произвольным заголовком Authorization (пустой — без заголовка).
func rawAuthRequest(t *testing.T, base, path, authorization string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	require.NoError(t, err)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, res.Header, b
}

// TestE2EAuth_TokenValidation: подпись, алгоритм, iss/aud, exp/nbf и допуск по времени (30s).
func TestE2EAuth_TokenValidation(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	list := widgetListPath()
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubPEM := publicKeyPEM(t, &standKey(t).PublicKey)

	with := func(mut func(c jwt.MapClaims)) jwt.MapClaims {
		c := baseClaims(time.Hour)
		c["sub"] = authClientSubject
		mut(c)
		return c
	}
	now := time.Now()
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"valid", signClaims(t, with(func(jwt.MapClaims) {})), http.StatusOK},
		{"wrong issuer", signClaims(t, with(func(c jwt.MapClaims) { c["iss"] = "https://evil.example" })), http.StatusUnauthorized},
		{"wrong audience", signClaims(t, with(func(c jwt.MapClaims) { c["aud"] = "other-service" })), http.StatusUnauthorized},
		{"foreign key", signWith(t, with(func(jwt.MapClaims) {}), jwt.SigningMethodRS256, otherKey, authKeyID), http.StatusUnauthorized},
		{"alg none", signWith(t, with(func(jwt.MapClaims) {}), jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, ""), http.StatusUnauthorized},
		{"HS256 with public key as secret", signWith(t, with(func(jwt.MapClaims) {}), jwt.SigningMethodHS256, pubPEM, authKeyID), http.StatusUnauthorized},
		{"no exp", signClaims(t, with(func(c jwt.MapClaims) { delete(c, "exp") })), http.StatusUnauthorized},
		{"nbf in future", signClaims(t, with(func(c jwt.MapClaims) { c["nbf"] = now.Add(time.Hour).Unix() })), http.StatusUnauthorized},
		{"expired within leeway", signClaims(t, with(func(c jwt.MapClaims) { c["exp"] = now.Add(-10 * time.Second).Unix() })), http.StatusOK},
		{"expired beyond leeway", signClaims(t, with(func(c jwt.MapClaims) { c["exp"] = now.Add(-2 * time.Minute).Unix() })), http.StatusUnauthorized},
		{"tampered payload", tamper(signClaims(t, with(func(jwt.MapClaims) {}))), http.StatusUnauthorized},
	}
	cl := rawClient(t)
	for _, c := range cases {
		code, b, err := cl.WithToken(c.token).Do(http.MethodGet, list, nil)
		require.NoError(t, err)
		require.Equal(t, c.want, code, "%s: %s", c.name, string(b))
	}
}

// tamper подменяет payload токена, сохраняя подпись.
func tamper(token string) string {
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	claims["sub"] = "someone-else"
	b, _ := json.Marshal(claims)
	parts[1] = base64.RawURLEncoding.EncodeToString(b)
	return strings.Join(parts, ".")
}

func publicKeyPEM(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// TestE2EAuth_AuthorizationHeader: схема Bearer без учёта регистра; другие схемы и пустой токен —
// 401 с WWW-Authenticate и JSON-телом; health и swagger открыты.
func TestE2EAuth_AuthorizationHeader(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	base := os.Getenv("E2E_STORAGE_URL")
	token := signToken(t, tokenOpts{Subject: authClientSubject})
	list := widgetListPath()

	code, _, _ := rawAuthRequest(t, base, list, "bearer "+token)
	require.Equal(t, http.StatusOK, code, "scheme is case-insensitive")
	code, _, _ = rawAuthRequest(t, base, list, "BEARER "+token)
	require.Equal(t, http.StatusOK, code)

	for _, h := range []string{"", "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p")), "Bearer ", "Bearer", token} {
		code, hdr, body := rawAuthRequest(t, base, list, h)
		require.Equal(t, http.StatusUnauthorized, code, "Authorization: %q", h)
		require.Equal(t, "Bearer", hdr.Get("WWW-Authenticate"))
		var e struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &e), string(body))
		require.NotEmpty(t, e.Error)
	}

	for _, p := range []string{"/health/live", "/health/ready", "/swagger/index.html"} {
		code, _, _ := rawAuthRequest(t, base, p, "")
		require.Equal(t, http.StatusOK, code, p)
	}
}

// TestE2EAuth_MalformedClaims: claims неверного типа или с невалидными правилами — 401.
func TestE2EAuth_MalformedClaims(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	list := widgetListPath()
	cases := map[string]map[string]any{
		"sub is a number":          {"sub": 42},
		"groups is a number":       {"sub": authClientSubject, "groups": 7},
		"groups with non-string":   {"sub": authClientSubject, "groups": []any{"ok", 1}},
		"permissions is an object": {"sub": authClientSubject, "permissions": map[string]any{"verbs": []string{"*"}}},
		"rule is not an object":    {"sub": authClientSubject, "permissions": []any{"get"}},
		"rule without verbs":       {"sub": authClientSubject, "permissions": []any{map[string]any{"kind": widgetKind}}},
		"rule with unknown verb":   {"sub": authClientSubject, "permissions": []any{map[string]any{"verbs": []string{"patch"}}}},
		"rule with empty verbs":    {"sub": authClientSubject, "permissions": []any{map[string]any{"verbs": []string{}}}},
		"rule field is not string": {"sub": authClientSubject, "permissions": []any{map[string]any{"verbs": []string{"*"}, "shard_id": 1}}},
	}
	for name, extra := range cases {
		c := baseClaims(time.Hour)
		for k, v := range extra {
			c[k] = v
		}
		code, b, err := rawClient(t).WithToken(signClaims(t, c)).Do(http.MethodGet, list, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, code, "%s: %s", name, string(b))
	}
}

// TestE2EAuth_VerbGranularity: каждый verb разрешает ровно свою операцию.
func TestE2EAuth_VerbGranularity(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	name := uniqueName("auth-verb")
	createWidget(t, admin, shard, name, nil)
	path := admin.resourcePath(name)
	list := widgetListPath("shard_id", shard)
	putBody := map[string]any{"shard_id": shard, "spec": map[string]any{}}
	statusBody := func() map[string]any {
		r, _ := getWidget(t, admin, name)
		return map[string]any{"shard_id": shard, "version": r.Version, "current_version": r.Version, "status": map[string]any{}}
	}

	only := func(verb string) *SMClient { return claimClient(t, "verb-"+verb, widgetRule(shard, verb)) }

	get := only("get")
	requireCode(t, get, http.StatusOK, http.MethodGet, path, nil)
	requireCode(t, get, http.StatusForbidden, http.MethodGet, list, nil)
	requireCode(t, get, http.StatusForbidden, http.MethodPut, path, putBody)

	lst := only("list")
	requireCode(t, lst, http.StatusOK, http.MethodGet, list, nil)
	requireCode(t, lst, http.StatusForbidden, http.MethodGet, path, nil)

	upd := only("update")
	requireCode(t, upd, http.StatusOK, http.MethodPut, path, putBody)
	requireCode(t, upd, http.StatusForbidden, http.MethodPut, path+"/status", statusBody())

	st := only("update_status")
	requireCode(t, st, http.StatusOK, http.MethodPut, path+"/status", statusBody())
	requireCode(t, st, http.StatusForbidden, http.MethodPut, path, putBody)

	crt := only("create")
	requireCode(t, crt, http.StatusCreated, http.MethodPost, admin.createPath(), map[string]any{"shard_id": shard, "name": uniqueName("auth-verb-new")})
	requireCode(t, crt, http.StatusForbidden, http.MethodGet, path, nil)

	del := only("delete")
	requireCode(t, del, http.StatusForbidden, http.MethodGet, path, nil)
	requireCode(t, del, http.StatusNoContent, http.MethodDelete, path, nil)
}

// TestE2EAuth_NameScopedRule: правило с name открывает только этот ресурс; list должен
// фильтровать по name, иначе фильтр не покрыт правилом.
func TestE2EAuth_NameScopedRule(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	mine, other := uniqueName("auth-name-mine"), uniqueName("auth-name-other")
	createWidget(t, admin, shard, mine, nil)
	createWidget(t, admin, shard, other, nil)

	rule := widgetRule("", "get", "list")
	rule["name"] = mine
	cl := claimClient(t, "name-scoped", rule)
	requireCode(t, cl, http.StatusOK, http.MethodGet, cl.resourcePath(mine), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, cl.resourcePath(other), nil)
	requireCode(t, cl, http.StatusOK, http.MethodGet, widgetListPath("name", mine), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, widgetListPath(), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, widgetListPath("name", other), nil)
}

// TestE2EAuth_WildcardRule: "*" в поле правила равносильно пустому полю — любое значение,
// в том числе list без фильтров.
func TestE2EAuth_WildcardRule(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	name := uniqueName("auth-wild")
	createWidget(t, admin, shard, name, nil)
	cl := claimClient(t, "wildcard", map[string]any{
		"verbs": []string{"get", "list"}, "resource_group": "*", "namespace": "*", "kind": "*", "name": "*", "shard_id": "*",
	})
	requireCode(t, cl, http.StatusOK, http.MethodGet, cl.resourcePath(name), nil)
	requireCode(t, cl, http.StatusOK, http.MethodGet, "/api/v1/resources", nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodDelete, cl.resourcePath(name), nil)
}

// TestE2EAuth_GroupRulesAreAdditive: права нескольких групп из БД складываются; groups может быть
// строкой, а не массивом.
func TestE2EAuth_GroupRulesAreAdditive(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	name := uniqueName("auth-groups")
	createWidget(t, admin, shard, name, nil)
	create := func() map[string]any { return map[string]any{"shard_id": shard, "name": uniqueName("auth-groups-new")} }

	both := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: "e2e-member", Groups: []string{authReadersGroup, authCreatorsGroup}}))
	requireCode(t, both, http.StatusOK, http.MethodGet, both.resourcePath(name), nil)
	requireCode(t, both, http.StatusCreated, http.MethodPost, both.createPath(), create())
	requireCode(t, both, http.StatusForbidden, http.MethodDelete, both.resourcePath(name), nil)

	creator := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: "e2e-member", Groups: []string{authCreatorsGroup}}))
	requireCode(t, creator, http.StatusCreated, http.MethodPost, creator.createPath(), create())
	requireCode(t, creator, http.StatusForbidden, http.MethodGet, creator.resourcePath(name), nil)

	c := baseClaims(time.Hour)
	c["sub"] = "e2e-member"
	c["groups"] = authReadersGroup
	str := rawClient(t).WithToken(signClaims(t, c))
	requireCode(t, str, http.StatusOK, http.MethodGet, str.resourcePath(name), nil)
}

// TestE2EAuth_DBShardScopedRule: правило из БД с shard_id ограничивает субъекта одним шардом.
func TestE2EAuth_DBShardScopedRule(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	cl := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: authShardSubject}))
	name := uniqueName("auth-dbshard")
	requireCode(t, cl, http.StatusCreated, http.MethodPost, cl.createPath(), map[string]any{"shard_id": authDBShard, "name": name})
	requireCode(t, cl, http.StatusForbidden, http.MethodPost, cl.createPath(), map[string]any{"shard_id": newShard(), "name": uniqueName("auth-dbshard")})
	requireCode(t, cl, http.StatusOK, http.MethodGet, cl.resourcePath(name), nil)
	requireCode(t, cl, http.StatusOK, http.MethodGet, widgetListPath("shard_id", authDBShard), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, widgetListPath(), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodPut, cl.resourcePath(name), map[string]any{"shard_id": newShard()})
	requireCode(t, cl, http.StatusNoContent, http.MethodDelete, cl.resourcePath(name), nil)
}

// TestE2EAuth_ClaimNarrowerThanDB: claim permissions заменяет права из БД, даже если он уже их.
func TestE2EAuth_ClaimNarrowerThanDB(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	name := uniqueName("auth-narrow")
	createWidget(t, admin, shard, name, nil)
	cl := claimClient(t, authClientSubject, widgetRule("", "get"))
	requireCode(t, cl, http.StatusOK, http.MethodGet, cl.resourcePath(name), nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodPost, cl.createPath(), map[string]any{"shard_id": shard, "name": uniqueName("auth-narrow")})
	requireCode(t, cl, http.StatusForbidden, http.MethodDelete, cl.resourcePath(name), nil)
}

// TestE2EAuth_MoveBetweenShards: перенос ресурса между шардами (PUT и PUT /status) разрешён,
// только если есть права на оба шарда.
func TestE2EAuth_MoveBetweenShards(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shardA, admin := setupShard(t)
	shardB := newShard()
	both := claimClient(t, "mover", widgetRule(shardA, "*"), widgetRule(shardB, "*"))
	name := uniqueName("auth-move")
	path := both.resourcePath(name)
	requireCode(t, both, http.StatusCreated, http.MethodPost, both.createPath(), map[string]any{"shard_id": shardA, "name": name})

	requireCode(t, both, http.StatusOK, http.MethodPut, path, map[string]any{"shard_id": shardB})
	r, _ := getWidget(t, admin, name)
	require.Equal(t, shardB, r.ShardID)

	requireCode(t, both, http.StatusOK, http.MethodPut, path+"/status",
		map[string]any{"shard_id": shardA, "version": r.Version, "current_version": r.Version})
	r, _ = getWidget(t, admin, name)
	require.Equal(t, shardA, r.ShardID)

	statusOnlyA := claimClient(t, "status-a", widgetRule(shardA, "update_status"))
	requireCode(t, statusOnlyA, http.StatusForbidden, http.MethodPut, path+"/status",
		map[string]any{"shard_id": shardB, "version": r.Version, "current_version": r.Version})
	r, _ = getWidget(t, admin, name)
	require.Equal(t, shardA, r.ShardID, "forbidden status update must not move the resource")
}

// TestE2EAuth_ForbiddenWritesHaveNoSideEffects: отказанные create и delete не создают ресурс,
// не ставят deletion_timestamp, не меняют version и не публикуют событий.
func TestE2EAuth_ForbiddenWritesHaveNoSideEffects(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	ownShard, admin := setupShard(t)
	foreign := newShard()
	op := rawClient(t).WithToken(operatorToken(t, ownShard))
	rdb := redisClient(t)

	created := uniqueName("auth-noside-create")
	requireCode(t, op, http.StatusForbidden, http.MethodPost, op.createPath(), map[string]any{"shard_id": foreign, "name": created})
	_, code, err := admin.GetResource(created)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, code)
	require.Empty(t, streamEvents(t, rdb, foreign), "forbidden create must not publish events")

	victim := uniqueName("auth-noside-delete")
	_, err = admin.Create(CreateOpts{ShardID: foreign, Name: victim, Finalizers: []string{"e2e.test/hold"}})
	require.NoError(t, err)
	requireCode(t, op, http.StatusForbidden, http.MethodDelete, op.resourcePath(victim), nil)
	r, _ := getWidget(t, admin, victim)
	require.Nil(t, r.DeletionTimestamp)
	require.Equal(t, 1, r.Version)
	require.Len(t, streamEvents(t, rdb, foreign), 1, "only the admin create event")
}

// TestE2EAuth_BindingChangesApplyImmediately: права из БД читаются на каждый запрос —
// новый binding работает сразу, выключенный или удалённый перестаёт работать сразу.
func TestE2EAuth_BindingChangesApplyImmediately(t *testing.T) {
	skipIfNoAuth(t)
	skipIfNoCompose(t)
	t.Parallel()
	shard, admin := setupShard(t)
	name := uniqueName("auth-bind")
	createWidget(t, admin, shard, name, nil)
	subject := "e2e-dyn-" + randHex(4)
	cl := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: subject}))
	path := cl.resourcePath(name)

	requireCode(t, cl, http.StatusForbidden, http.MethodGet, path, nil)
	psql(t, fmt.Sprintf(`INSERT INTO auth_role_bindings (role_id, subject_kind, subject_value)
		SELECT id, 'subject', '%s' FROM auth_roles WHERE name = 'e2e-widgets-reader'`, subject))
	requireCode(t, cl, http.StatusOK, http.MethodGet, path, nil)
	psql(t, fmt.Sprintf(`UPDATE auth_role_bindings SET disabled = true WHERE subject_value = '%s'`, subject))
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, path, nil)
	psql(t, fmt.Sprintf(`UPDATE auth_role_bindings SET disabled = false WHERE subject_value = '%s'`, subject))
	requireCode(t, cl, http.StatusOK, http.MethodGet, path, nil)
	psql(t, fmt.Sprintf(`DELETE FROM auth_role_bindings WHERE subject_value = '%s'`, subject))
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, path, nil)
}

// TestE2EAuth_ExistenceNotLeaked: субъект без прав не должен отличать существующий ресурс от
// несуществующего по коду ответа.
//
// Известный баг state-manager: права проверяются после загрузки ресурса — для отсутствующего
// GET отдаёт 404 (а DELETE / PUT — 500), для существующего — 403.
func TestE2EAuth_ExistenceNotLeaked(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	knownBug(t, "state-manager reveals resource existence to callers without permissions (404/500 vs 403)")
	shard, admin := setupShard(t)
	existing := uniqueName("auth-leak")
	createWidget(t, admin, shard, existing, nil)
	missing := uniqueName("auth-leak-missing")
	nobody := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: "e2e-unknown"}))

	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		var body any
		if m == http.MethodPut {
			body = map[string]any{"shard_id": shard}
		}
		c1, _, err := nobody.Do(m, nobody.resourcePath(existing), body)
		require.NoError(t, err)
		c2, _, err := nobody.Do(m, nobody.resourcePath(missing), body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, c1, m)
		require.Equal(t, c1, c2, "%s: existing and missing resources must look the same", m)
	}
}

// ---------------------------------------------------------------------------
// Оператор под авторизацией
// ---------------------------------------------------------------------------

// TestE2EAuth_OperatorForeignShardToken: токен оператора выписан на другой шард — ListPending
// получает 403, mgr.Run возвращает ошибку, процесс завершается с ненулевым кодом.
func TestE2EAuth_OperatorForeignShardToken(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard := newShard()
	op := startOperator(t, operatorOpts{Shard: shard, Token: strPtr(operatorToken(t, newShard())), NoWaitReady: true})
	code, ok := op.WaitExit(45 * time.Second)
	require.True(t, ok, "operator must exit; logs:\n%s", op.Logs())
	require.NotEqual(t, 0, code)
	require.Contains(t, strings.ToLower(op.Logs()), "forbidden")
}

// TestE2EAuth_OperatorWithoutToken: без токена при включённой авторизации оператор падает на старте (401).
func TestE2EAuth_OperatorWithoutToken(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	op := startOperator(t, operatorOpts{Token: strPtr(""), NoWaitReady: true})
	code, ok := op.WaitExit(45 * time.Second)
	require.True(t, ok, "operator must exit; logs:\n%s", op.Logs())
	require.NotEqual(t, 0, code)
	require.Contains(t, op.Logs(), "missing bearer token")
}

// TestE2EAuth_OperatorWithoutUpdateStatus: без update_status оператор стартует, но не может
// сохранить результат — ресурс не становится Ready, процесс продолжает работать.
func TestE2EAuth_OperatorWithoutUpdateStatus(t *testing.T) {
	skipIfNoAuth(t)
	t.Parallel()
	shard, admin := setupShard(t)
	perms := []map[string]any{{"verbs": []string{"get", "list", "create", "update", "delete"}, "shard_id": shard}}
	token := signToken(t, tokenOpts{Subject: "e2e-operator-limited", Permissions: perms})
	op := startOperator(t, operatorOpts{Shard: shard, Token: &token})

	name := uniqueName("auth-op-nostatus")
	createWidget(t, admin, shard, name, nil)
	require.Eventually(t, func() bool { return strings.Contains(strings.ToLower(op.Logs()), "forbidden") }, 15*time.Second, pollStep)
	requireNever(t, 3*time.Second, func() bool {
		r, _ := getWidget(t, admin, name)
		return r.CurrentVersion != 0 || len(r.Finalizers) != 0 || hasStatus(r)
	}, "nothing may be written without update_status")
	require.False(t, op.Exited())
}

// ---------------------------------------------------------------------------
// Конфигурация
// ---------------------------------------------------------------------------

// TestE2EAuth_InvalidConfigFailsStartup: невалидная конфигурация авторизации не даёт
// state-manager стартовать (а не запускает его без проверки токенов).
func TestE2EAuth_InvalidConfigFailsStartup(t *testing.T) {
	skipIfNoAuth(t)
	skipIfNoCompose(t)
	t.Parallel()
	cases := []struct {
		name string
		env  []string
		log  string
	}{
		{"symmetric algorithm", []string{"AUTH_ALGORITHMS=HS256"}, "unsupported algorithm"},
		{"unknown permissions source", []string{"AUTH_PERMISSIONS_SOURCE=bogus"}, "unknown AUTH_PERMISSIONS_SOURCE"},
		{"two key sources", []string{"AUTH_JWKS_URL=http://127.0.0.1:1/jwks.json"}, "exactly one of"},
		{"missing key file", []string{"AUTH_JWT_PUBLIC_KEY_FILE=/nonexistent.pem"}, "failed to init token verifier"},
		{"invalid AUTH_ENABLED", []string{"AUTH_ENABLED=maybe"}, "invalid AUTH_ENABLED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := []string{"run", "--rm", "--no-deps"}
			for _, e := range c.env {
				args = append(args, "-e", e)
			}
			args = append(args, "state-manager")
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			out, err := composeCtx(ctx, args...)
			require.Error(t, err, "state-manager must exit with an error; output:\n%s", out)
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "must exit by itself, not by timeout; output:\n%s", out)
			require.Contains(t, out, c.log)
		})
	}
}
