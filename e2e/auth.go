package e2e

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// Субъекты из fixtures/auth/seed.sql.
const (
	authClientSubject   = "e2e-client"   // все права на виджеты в namespace default (из БД)
	authReadersGroup    = "e2e-readers"  // get/list виджетов (из БД, binding на группу)
	authDisabledSubject = "e2e-disabled" // binding выключен
)

// authEnabled — state-manager запущен с авторизацией (фаза auth в scripts/e2e.sh).
func authEnabled() bool {
	return os.Getenv("E2E_AUTH") != ""
}

// skipIfNoAuth — тесты, которым нужна авторизация, выполняются только в фазе auth.
func skipIfNoAuth(t *testing.T) {
	t.Helper()
	skipIfNoEnv(t)
	if !authEnabled() {
		t.Skip("authorization is disabled (E2E_AUTH is not set)")
	}
}

// tokenOpts — содержимое тестового JWT. Permissions == nil — claim не добавляется
// (права берутся из БД), пустой срез — claim есть, но пустой.
type tokenOpts struct {
	Subject     string
	Groups      []string
	Permissions []map[string]any
	ExpiresIn   time.Duration
}

// signToken подписывает JWT ключом стенда (E2E_AUTH_PRIVATE_KEY), как это делал бы IdP.
func signToken(t *testing.T, o tokenOpts) string {
	t.Helper()
	pemBytes, err := os.ReadFile(os.Getenv("E2E_AUTH_PRIVATE_KEY"))
	require.NoError(t, err, "E2E_AUTH_PRIVATE_KEY")
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	require.NoError(t, err)

	if o.ExpiresIn == 0 {
		o.ExpiresIn = time.Hour
	}
	claims := jwt.MapClaims{
		"iss": os.Getenv("E2E_AUTH_ISSUER"),
		"aud": os.Getenv("E2E_AUTH_AUDIENCE"),
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(o.ExpiresIn).Unix(),
	}
	if o.Subject != "" {
		claims["sub"] = o.Subject
	}
	if o.Groups != nil {
		claims["groups"] = o.Groups
	}
	if o.Permissions != nil {
		claims["permissions"] = o.Permissions
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	require.NoError(t, err)
	return s
}

// operatorToken — токен оператора шарда: права в claim, ограничены его shard_id.
func operatorToken(t *testing.T, shard string) string {
	t.Helper()
	return signToken(t, tokenOpts{Subject: "e2e-operator-" + shard, Permissions: shardPermissions(shard)})
}

// shardPermissions — права контроллера шарда: всё в пределах своего shard_id (в claim, без БД).
func shardPermissions(shard string) []map[string]any {
	return []map[string]any{{"verbs": []string{"*"}, "shard_id": shard}}
}
