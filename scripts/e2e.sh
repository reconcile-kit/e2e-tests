#!/usr/bin/env bash
# E2E прогоняется фазами, каждая — на чистом стенде (docker compose down -v между фазами):
#   1. no-auth — state-manager без авторизации: базовые тесты (auth-тесты пропускаются);
#   2. auth    — state-manager с AUTH_ENABLED=true (docker-compose.auth.yml): базовые тесты
#                с JWT + тесты авторизации (E2E_AUTH=1).
# Падение фазы останавливает прогон.
#
# Переменные:
#   E2E_PHASES      — какие фазы запускать (по умолчанию "no-auth auth");
#   E2E_RUN         — фильтр go test -run;
#   E2E_KNOWN_BUGS  — 1: запускать тесты известных багов библиотек (по умолчанию пропускаются);
#   E2E_OPERATOR_LOGS — 1: печатать логи операторов сразу (иначе только у упавших тестов).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-reconcile-kit-e2e}"
COMPOSE=(docker compose -f docker-compose.yml)

cleanup() {
	"${COMPOSE[@]}" down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

"${ROOT}/scripts/prepare-deps.sh"

# Ключ «внешнего IdP» для фазы auth: публичный монтируется в state-manager,
# приватным тесты подписывают JWT.
mkdir -p "${ROOT}/build/auth"
openssl genrsa -out "${ROOT}/build/auth/private.pem" 2048 2>/dev/null
openssl rsa -in "${ROOT}/build/auth/private.pem" -pubout -out "${ROOT}/build/auth/public.pem" 2>/dev/null
chmod 644 "${ROOT}/build/auth/public.pem"

docker compose build state-manager

mkdir -p "${ROOT}/bin"
# Флаг -C должен быть сразу после «go», иначе workspace не подхватится как нужно.
go -C "${ROOT}/fixtures/e2e-operator" mod tidy
go -C "${ROOT}/fixtures/e2e-operator" build -o "${ROOT}/bin/e2e-operator" ./cmd
go -C "${ROOT}/e2e" mod tidy

# В режиме E2E_KNOWN_BUGS тесты известных багов заведомо падают: не прерываем прогон,
# а возвращаем ошибку в конце.
FAILED=0
check() {
	if "$@"; then
		return 0
	fi
	if [[ -n "${E2E_KNOWN_BUGS:-}" ]]; then
		FAILED=1
		return 0
	fi
	exit 1
}

# Unit-тесты фикстуры оператора (без стенда).
check go -C "${ROOT}/fixtures/e2e-operator" test -count=1 ./...

export E2E_STORAGE_URL="http://127.0.0.1:58080"
export E2E_INFORMER_URL="127.0.0.1:56379"
export E2E_SHARD_ID="e2e-shard-1"
export E2E_OPERATOR_BIN="${ROOT}/bin/e2e-operator"
# Для тестов, которые управляют стендом (рестарт контейнеров, psql).
export E2E_ROOT="${ROOT}"

# Ждём HTTP state-manager
wait_state_manager() {
	for i in $(seq 1 60); do
		if curl -sf "http://127.0.0.1:58080/health/live" >/dev/null 2>&1; then
			echo "state-manager is up"
			return 0
		fi
		sleep 1
	done
	echo "timeout waiting for state-manager"
	"${COMPOSE[@]}" logs state-manager
	exit 1
}

start_stand() {
	"${COMPOSE[@]}" up -d
	"${COMPOSE[@]}" ps
	wait_state_manager
}

run_tests() {
	# Файлы compose текущей фазы — тестам, которые сами вызывают docker compose.
	local files=()
	local i
	for ((i = 2; i < ${#COMPOSE[@]}; i += 2)); do
		files+=("${COMPOSE[i+1]}")
	done
	E2E_COMPOSE_FILES="$(IFS=:; echo "${files[*]}")" \
		go -C "${ROOT}/e2e" test -count=1 -v -timeout=20m ${E2E_RUN:+-run "${E2E_RUN}"} ./...
}

PHASES="${E2E_PHASES:-no-auth auth}"
phase_enabled() {
	[[ " ${PHASES} " == *" $1 "* ]]
}

if phase_enabled no-auth; then
	echo "=== Phase 1: no-auth"
	start_stand
	check run_tests
	cleanup
fi

finish() {
	if [[ "${FAILED}" -ne 0 ]]; then
		echo "e2e: FAILED (known bugs mode)"
		exit 1
	fi
	echo "e2e: OK"
	exit 0
}

if ! phase_enabled auth; then
	finish
fi

echo "=== Phase 2: auth"
COMPOSE+=(-f docker-compose.auth.yml)
start_stand
# health/live отвечает после миграций, таблицы auth_* уже есть.
"${COMPOSE[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -q -U e2e -d e2e < "${ROOT}/fixtures/auth/seed.sql"
E2E_AUTH=1 \
	E2E_AUTH_PRIVATE_KEY="${ROOT}/build/auth/private.pem" \
	E2E_AUTH_ISSUER="https://e2e-idp.reconcile-kit.dev" \
	E2E_AUTH_AUDIENCE="state-manager" \
	check run_tests

finish
