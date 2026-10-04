#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

cleanup() {
	docker compose down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

"${ROOT}/scripts/prepare-deps.sh"

# Ключ «внешнего IdP» для state-manager-auth: публичный монтируется в контейнер,
# приватным тесты подписывают JWT.
mkdir -p "${ROOT}/build/auth"
openssl genrsa -out "${ROOT}/build/auth/private.pem" 2048 2>/dev/null
openssl rsa -in "${ROOT}/build/auth/private.pem" -pubout -out "${ROOT}/build/auth/public.pem" 2>/dev/null
chmod 644 "${ROOT}/build/auth/public.pem"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-reconcile-kit-e2e}"
docker compose build state-manager state-manager-auth
docker compose up -d
docker compose ps

# Ждём HTTP обоих state-manager
wait_live() {
	local service="$1" port="$2"
	for i in $(seq 1 60); do
		if curl -sf "http://127.0.0.1:${port}/health/live" >/dev/null 2>&1; then
			echo "${service} is up"
			return 0
		fi
		sleep 1
	done
	echo "timeout waiting for ${service}"
	docker compose logs "${service}"
	exit 1
}
wait_live state-manager 58080
wait_live state-manager-auth 58081

# health/live отвечает после миграций, таблицы auth_* уже есть.
docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -q -U e2e -d e2e_auth < "${ROOT}/fixtures/auth/seed.sql"

mkdir -p "${ROOT}/bin"
# Флаг -C должен быть сразу после «go», иначе workspace не подхватится как нужно.
go -C "${ROOT}/fixtures/e2e-operator" mod tidy
go -C "${ROOT}/fixtures/e2e-operator" build -o "${ROOT}/bin/e2e-operator" ./cmd

export E2E_STORAGE_URL="http://127.0.0.1:58080"
export E2E_INFORMER_URL="127.0.0.1:56379"
export E2E_SHARD_ID="e2e-shard-1"
export E2E_OPERATOR_BIN="${ROOT}/bin/e2e-operator"
export E2E_AUTH_STORAGE_URL="http://127.0.0.1:58081"
export E2E_AUTH_SHARD_ID="e2e-auth-shard-1"
export E2E_AUTH_PRIVATE_KEY="${ROOT}/build/auth/private.pem"
export E2E_AUTH_ISSUER="https://e2e-idp.reconcile-kit.dev"
export E2E_AUTH_AUDIENCE="state-manager"

go -C "${ROOT}/e2e" mod tidy
go -C "${ROOT}/e2e" test -count=1 -v -timeout=15m ./...

echo "e2e: OK"
