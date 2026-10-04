package e2e

import (
	"crypto/rsa"
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
	authCreatorsGroup   = "e2e-creators" // только create виджетов (из БД, binding на группу)
	authShardSubject    = "e2e-shard-client"
	authDBShard         = "e2e-db-shard" // единственный шард, доступный authShardSubject
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

// authKeyID — kid ключа стенда (как его выставил бы IdP; верификатор с ключом из файла его не проверяет).
const authKeyID = "e2e-key-1"

// signToken подписывает JWT ключом стенда (E2E_AUTH_PRIVATE_KEY), как это делал бы IdP.
func signToken(t *testing.T, o tokenOpts) string {
	t.Helper()
	if o.ExpiresIn == 0 {
		o.ExpiresIn = time.Hour
	}
	claims := baseClaims(o.ExpiresIn)
	if o.Subject != "" {
		claims["sub"] = o.Subject
	}
	if o.Groups != nil {
		claims["groups"] = o.Groups
	}
	if o.Permissions != nil {
		claims["permissions"] = o.Permissions
	}
	return signClaims(t, claims)
}

// baseClaims — стандартные claims валидного токена стенда.
func baseClaims(expiresIn time.Duration) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": os.Getenv("E2E_AUTH_ISSUER"),
		"aud": os.Getenv("E2E_AUTH_AUDIENCE"),
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(expiresIn).Unix(),
	}
}

// signClaims подписывает произвольные claims ключом стенда (RS256, kid стенда).
func signClaims(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	return signWith(t, claims, jwt.SigningMethodRS256, standKey(t), authKeyID)
}

// signWith подписывает claims заданным методом и ключом; kid пустой — без заголовка kid.
func signWith(t *testing.T, claims jwt.MapClaims, method jwt.SigningMethod, key any, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

// standKey — приватный ключ «IdP» стенда.
func standKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	pemBytes, err := os.ReadFile(os.Getenv("E2E_AUTH_PRIVATE_KEY"))
	require.NoError(t, err, "E2E_AUTH_PRIVATE_KEY")
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	require.NoError(t, err)
	return key
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
