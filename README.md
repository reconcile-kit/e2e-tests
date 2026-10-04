# E2E-тесты reconcile-kit

Интеграционные тесты: Docker (Postgres, Redis, state-manager), оператор из `fixtures/e2e-operator`, пакет `e2e/`.

## Переменные `*_REF` (версии зависимостей)

Скрипт `scripts/prepare-deps.sh` подтягивает репозитории reconcile-kit в `build/deps/`. Для каждого репо можно задать свою переменную (`API_REF`, `CONTROLLOOP_REF`, `STATE_MANAGER_PROVIDER_REF`, `REDIS_INFORMER_PROVIDER_REF`, `RUNTIME_MANAGER_REF`, `STATE_MANAGER_REF`).

**Если не задать ни одной `*_REF`** (просто `make e2e`), для **всех шести** репозиториев используется ветка **`main`**: клоны с GitHub `reconcile-kit/<repo>`.

**Повторный запуск** при уже существующем `build/deps/<repo>`: для **веток** делается `fetch` и выравнивание на актуальный `origin/<ветка>` (в том числе если `main` ушёл вперёд). Для **SHA** при shallow-клоне при необходимости выполняется `unshallow` / `deepen` и повторный `fetch`, чтобы нужный коммит стал доступен.

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

## Фазы: без авторизации и с авторизацией

`make e2e` прогоняет тесты последовательно в две фазы, каждая на чистом стенде (`docker compose down -v` между фазами).
Падение фазы останавливает прогон.

| Фаза      | state-manager                                                      | Тесты                                         |
|-----------|--------------------------------------------------------------------|-----------------------------------------------|
| `no-auth` | без авторизации (поведение по умолчанию)                           | базовые (`integration_test.go`)               |
| `auth`    | `AUTH_ENABLED=true` через `docker-compose.auth.yml`, `E2E_AUTH=1` | базовые с JWT + авторизация (`auth_test.go`)  |

Базовые тесты не зависят от режима. В фазе `auth`:

- скрипт генерирует RSA-ключ «внешнего IdP» в `build/auth`: публичный монтируется в state-manager, приватным тесты подписывают токены;
- права из БД заливаются из `fixtures/auth/seed.sql`;
- тестовый клиент ходит с токеном без прав в claim — права берутся из БД по `sub`;
- оператор получает токен с правами в claim, ограниченными своим `shard_id` (`E2E_OPERATOR_TOKEN`).

`e2e/auth_test.go` выполняется только в фазе `auth` и проверяет 401/403: невалидные токены, изоляцию шардов
(в т.ч. запрет переноса ресурса в чужой шард), покрытие фильтра list, права из БД по `sub` и группе,
выключенный binding и приоритет claim над БД.

## Требования

Go (см. `go.work`), Docker и Docker Compose.