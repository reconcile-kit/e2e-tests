#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

cleanup() {
	docker compose down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

"${ROOT}/scripts/prepare-deps.sh"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-reconcile-kit-e2e}"
docker compose build state-manager
docker compose up -d
docker compose ps

# Ждём HTTP state-manager
for i in $(seq 1 60); do
	if curl -sf "http://127.0.0.1:58080/health/live" >/dev/null 2>&1; then
		echo "state-manager is up"
		break
	fi
	if [[ "${i}" -eq 60 ]]; then
		echo "timeout waiting for state-manager"
		docker compose logs state-manager
		exit 1
	fi
	sleep 1
done

mkdir -p "${ROOT}/bin"
# Флаг -C должен быть сразу после «go», иначе workspace не подхватится как нужно.
go -C "${ROOT}/fixtures/e2e-operator" mod tidy
go -C "${ROOT}/fixtures/e2e-operator" build -o "${ROOT}/bin/e2e-operator" ./cmd

export E2E_STORAGE_URL="http://127.0.0.1:58080"
export E2E_INFORMER_URL="127.0.0.1:56379"
export E2E_SHARD_ID="e2e-shard-1"
export E2E_OPERATOR_BIN="${ROOT}/bin/e2e-operator"

go -C "${ROOT}/e2e" mod tidy
go -C "${ROOT}/e2e" test -count=1 -v -timeout=15m ./...

echo "e2e: OK"
