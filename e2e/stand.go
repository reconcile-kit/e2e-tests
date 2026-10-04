package e2e

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// stand — один поднятый state-manager. Сценарии прогоняются на всех стендах:
//   - no-auth: state-manager без авторизации (поведение по умолчанию);
//   - auth:    тот же state-manager с AUTH_ENABLED=true, клиент и оператор ходят с JWT.
type stand struct {
	name       string
	storageURL string
	shard      string
	auth       bool
}

func noAuthStand() *stand {
	shard := os.Getenv("E2E_SHARD_ID")
	if shard == "" {
		shard = "e2e-shard-1"
	}
	return &stand{name: "no-auth", storageURL: os.Getenv("E2E_STORAGE_URL"), shard: shard}
}

func authStandFromEnv() *stand {
	return &stand{
		name:       "auth",
		storageURL: os.Getenv("E2E_AUTH_STORAGE_URL"),
		shard:      os.Getenv("E2E_AUTH_SHARD_ID"),
		auth:       true,
	}
}

// forEachStand запускает сценарий подтестом на каждом стенде.
func forEachStand(t *testing.T, fn func(t *testing.T, st *stand)) {
	t.Helper()
	for _, st := range []*stand{noAuthStand(), authStandFromEnv()} {
		t.Run(st.name, func(t *testing.T) {
			if st.storageURL == "" {
				t.Skipf("stand %s is not configured (run ./scripts/e2e.sh)", st.name)
			}
			fn(t, st)
		})
	}
}

// authStand возвращает стенд с авторизацией или пропускает тест.
func authStand(t *testing.T) *stand {
	t.Helper()
	st := authStandFromEnv()
	if st.storageURL == "" {
		t.Skip("set E2E_AUTH_STORAGE_URL (run ./scripts/e2e.sh)")
	}
	return st
}

// rawClient — клиент стенда без токена.
func (st *stand) rawClient(t *testing.T) *SMClient {
	t.Helper()
	cl, err := NewSMClient(st.storageURL)
	require.NoError(t, err)
	return cl
}

// client — клиент тестов: на auth-стенде с токеном, права которого берутся из БД по sub.
func (st *stand) client(t *testing.T) *SMClient {
	t.Helper()
	cl := st.rawClient(t)
	if st.auth {
		cl = cl.WithToken(signToken(t, tokenOpts{Subject: authClientSubject}))
	}
	return cl
}

// operatorToken — токен оператора шарда: права в claim, ограничены его shard_id.
// На стенде без авторизации пустой.
func (st *stand) operatorToken(t *testing.T, shard string) string {
	t.Helper()
	if !st.auth {
		return ""
	}
	return signToken(t, tokenOpts{Subject: "e2e-operator-" + shard, Permissions: shardPermissions(shard)})
}
