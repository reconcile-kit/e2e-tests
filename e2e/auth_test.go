package e2e

import (
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Проверки, которым нужна авторизация: выполняются только в фазе auth scripts/e2e.sh (E2E_AUTH=1). Права:
//   - оператор шарда — inline в claim токена (без похода в БД), только свой shard_id;
//   - e2e-client — из БД по sub: всё над виджетами в namespace default;
//   - группа e2e-readers — из БД: get/list виджетов;
//   - e2e-disabled — binding выключен.

func listPath(filters map[string]string) string {
	q := url.Values{}
	for k, v := range filters {
		q.Set(k, v)
	}
	return "/api/v1/resources?" + q.Encode()
}

// rawClient — клиент без токена.
func rawClient(t *testing.T) *SMClient {
	t.Helper()
	cl, err := NewSMClient(os.Getenv("E2E_STORAGE_URL"))
	require.NoError(t, err)
	return cl
}

func requireCode(t *testing.T, cl *SMClient, want int, method, path string, body any) {
	t.Helper()
	code, b, err := cl.Do(method, path, body)
	require.NoError(t, err)
	require.Equal(t, want, code, "%s %s: %s", method, path, string(b))
}

// TestE2EAuth_Unauthenticated: без токена и с невалидными токенами — 401, health открыт.
func TestE2EAuth_Unauthenticated(t *testing.T) {
	skipIfNoAuth(t)
	shard, _ := setupE2E(t)
	cl := rawClient(t)
	list := listPath(map[string]string{"shard_id": shard})

	requireCode(t, cl, http.StatusOK, http.MethodGet, "/health/live", nil)
	requireCode(t, cl, http.StatusUnauthorized, http.MethodGet, list, nil)
	requireCode(t, cl.WithToken("not-a-jwt"), http.StatusUnauthorized, http.MethodGet, list, nil)

	expired := signToken(t, tokenOpts{Subject: authClientSubject, ExpiresIn: -time.Hour})
	requireCode(t, cl.WithToken(expired), http.StatusUnauthorized, http.MethodGet, list, nil)
}

// TestE2EAuth_ShardTokenFromClaim: токен оператора видит только свой шард, list обязан
// фильтровать по shard_id (фильтр должен быть покрыт правилом).
func TestE2EAuth_ShardTokenFromClaim(t *testing.T) {
	skipIfNoAuth(t)
	shard, _ := setupE2E(t)
	op := rawClient(t).WithToken(operatorToken(t, shard))

	requireCode(t, op, http.StatusOK, http.MethodGet, listPath(map[string]string{
		"pending": "true", "resource_group": widgetGroup, "kind": widgetKind, "shard_id": shard,
	}), nil)
	requireCode(t, op, http.StatusForbidden, http.MethodGet, listPath(map[string]string{
		"resource_group": widgetGroup, "kind": widgetKind,
	}), nil)
	requireCode(t, op, http.StatusForbidden, http.MethodGet, listPath(map[string]string{
		"shard_id": "e2e-auth-foreign-shard",
	}), nil)

	name := "auth-own-" + time.Now().Format("150405.000")
	requireCode(t, op, http.StatusCreated, http.MethodPost, op.createPath(),
		map[string]any{"name": name, "shard_id": shard, "spec": map[string]any{}})
	requireCode(t, op, http.StatusForbidden, http.MethodPost, op.createPath(),
		map[string]any{"name": name + "-x", "shard_id": "e2e-auth-foreign-shard", "spec": map[string]any{}})

	// Свой ресурс нельзя увести в чужой шард.
	requireCode(t, op, http.StatusForbidden, http.MethodPut, op.resourcePath(name),
		map[string]any{"shard_id": "e2e-auth-foreign-shard", "spec": map[string]any{}})
	requireCode(t, op, http.StatusOK, http.MethodGet, op.resourcePath(name), nil)
	requireCode(t, op, http.StatusNoContent, http.MethodDelete, op.resourcePath(name), nil)
}

// TestE2EAuth_ForeignShardResource: ресурс чужого шарда закрыт для оператора на чтение,
// изменение (в т.ч. перенос в свой шард), статус и удаление; shard_id берётся из БД.
func TestE2EAuth_ForeignShardResource(t *testing.T) {
	skipIfNoAuth(t)
	shard, cl := setupE2E(t)
	op := rawClient(t).WithToken(operatorToken(t, shard))

	name := "auth-foreign-" + time.Now().Format("150405.000")
	_, err := cl.CreateResource("e2e-auth-foreign-shard", name, []byte(`{}`), nil)
	require.NoError(t, err)

	path := op.resourcePath(name)
	requireCode(t, op, http.StatusForbidden, http.MethodGet, path, nil)
	requireCode(t, op, http.StatusForbidden, http.MethodPut, path,
		map[string]any{"shard_id": shard, "spec": map[string]any{}})
	requireCode(t, op, http.StatusForbidden, http.MethodPut, path+"/status",
		map[string]any{"shard_id": shard, "version": 1, "status": map[string]any{}})
	requireCode(t, op, http.StatusForbidden, http.MethodDelete, path, nil)

	r, code, err := cl.GetResource(name)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "e2e-auth-foreign-shard", r.ShardID, "resource must stay in its shard")
	require.NoError(t, cl.DeleteResource(name))
}

// TestE2EAuth_DBRules: токены без claim permissions получают права из БД по sub и группам.
func TestE2EAuth_DBRules(t *testing.T) {
	skipIfNoAuth(t)
	shard, cl := setupE2E(t)
	name := "auth-db-" + time.Now().Format("150405.000")
	_, err := cl.CreateResource(shard, name, []byte(`{}`), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.DeleteResource(name) })

	byNamespace := listPath(map[string]string{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace})
	byKind := listPath(map[string]string{"resource_group": widgetGroup, "kind": widgetKind})

	// sub=e2e-client: правило на group+namespace+kind.
	requireCode(t, cl, http.StatusOK, http.MethodGet, byNamespace, nil)
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, byKind, nil)

	// Группа e2e-readers: только чтение.
	reader := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: "e2e-someone", Groups: []string{authReadersGroup}}))
	requireCode(t, reader, http.StatusOK, http.MethodGet, reader.resourcePath(name), nil)
	requireCode(t, reader, http.StatusOK, http.MethodGet, byKind, nil)
	requireCode(t, reader, http.StatusForbidden, http.MethodPost, reader.createPath(),
		map[string]any{"name": name + "-r", "shard_id": shard, "spec": map[string]any{}})
	requireCode(t, reader, http.StatusForbidden, http.MethodDelete, reader.resourcePath(name), nil)

	// Нет binding'ов или binding выключен — прав нет.
	for _, sub := range []string{"e2e-unknown", authDisabledSubject} {
		other := rawClient(t).WithToken(signToken(t, tokenOpts{Subject: sub}))
		requireCode(t, other, http.StatusForbidden, http.MethodGet, other.resourcePath(name), nil)
	}
}

// TestE2EAuth_ClaimOverridesDB: если в токене есть claim permissions (даже пустой),
// права из БД не используются (AUTH_PERMISSIONS_SOURCE=auto).
func TestE2EAuth_ClaimOverridesDB(t *testing.T) {
	skipIfNoAuth(t)
	cl := rawClient(t).WithToken(signToken(t, tokenOpts{
		Subject:     authClientSubject,
		Permissions: []map[string]any{},
	}))
	requireCode(t, cl, http.StatusForbidden, http.MethodGet, listPath(map[string]string{
		"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace,
	}), nil)
}
