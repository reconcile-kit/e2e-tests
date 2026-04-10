# E2E-тесты reconcile-kit

Интеграционные тесты: Docker (Postgres, Redis, state-manager), оператор из `fixtures/e2e-operator`, пакет `e2e/`.

## Переменные `*_REF` (версии зависимостей)

Скрипт `scripts/prepare-deps.sh` подтягивает репозитории reconcile-kit в `build/deps/`. Для каждого репо можно задать свою переменную (`API_REF`, `CONTROLLOOP_REF`, `STATE_MANAGER_PROVIDER_REF`, `REDIS_INFORMER_PROVIDER_REF`, `RUNTIME_MANAGER_REF`, `STATE_MANAGER_REF`).

**Если не задать ни одной `*_REF`** (просто `make e2e`), для **всех шести** репозиториев используется ветка **`main`**: клоны/чекаут с GitHub `reconcile-kit/<repo>` на `main`.

### Примеры запуска `make e2e`

Все ref задаются в одной строке перед `make e2e` . Форматы можно **смешивать**.

```bash
# Один репо по SHA, другой — локальный каталог с go.mod; остальные — main
STATE_MANAGER_REF=05a52e7a138f8bdf887bc470f5249609c58c212a CONTROLLOOP_REF=$HOME/GolandProjects/controlloop make e2e

# Один репо из PR, остальные — main (переменные не заданы)
STATE_MANAGER_REF=pr/17 make e2e

# Несколько локальных модулей
API_REF=$HOME/ws/api REDIS_INFORMER_PROVIDER_REF=../redis-informer-provider make e2e

# Явно задать ветку для части репозиториев
API_REF=v0.2.0 CONTROLLOOP_REF=v0.2.0 make e2e

```

Шаблон для файла — `versions.env.example`:

```bash
cp versions.env.example versions.env
# отредактировать нужные *_REF
set -a && source versions.env && set +a && make prepare-deps
# затем при необходимости: make e2e
```

## Полный прогон

```bash
make e2e
```

## Требования

Go (см. `go.work`), Docker и Docker Compose.
