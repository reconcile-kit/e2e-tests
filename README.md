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

## Состав тестов

| Файл | Что проверяет |
|------|---------------|
| `e2e/integration_test.go` | базовые сценарии: создание → Ready, Requeue, удаление с финализатором, List по лейблам в reconcile |
| `e2e/operator_test.go` | полный контур с оператором: события Redis, обновление spec и конфликт версий, ошибки/паники/RequeueAfter, рестарт, шардирование, несколько kind, дочерние ресурсы, RemoteClient, параллельные воркеры, graceful shutdown, ошибки конфигурации |
| `e2e/contract_test.go` | HTTP-контракт state-manager: коды ответов, версии, лейблы, удаление, list и label selector |
| `e2e/events_test.go` | события в Redis Streams и их обработка информером |
| `e2e/chaos_test.go` | рестарт state-manager и Redis под работающим оператором (только фаза `no-auth`) |
| `e2e/common_watchedby_test.go` | `E2EConfig → E2EConsumer`: оба notifier, name/labels, AND-фильтры, namespace/kind/shard, 501 потребитель, повторные уведомления, обработка после запуска потребителя |
| `e2e/common_client_test.go`, `e2e/common_errors_test.go` | unstructured CRUD, JSON и metadata, версии и события, ошибки авторизации и восстановление после рестарта, регрессия исчезнувшей цели |
| `fixtures/e2e-operator/common/*_test.go` | контракты всех пакетов `controlloop/common`: пагинация, селекторы, отмена, ошибки, частичные уведомления, metadata и DeepCopy |
| `e2e/auth_test.go`, `e2e/auth_more_test.go` | авторизация (только фаза `auth`) |
| `fixtures/e2e-operator/**/*_test.go` | unit-тесты фикстуры и поведения `api` (без стенда) |

Тесты, которым нужен оператор, работают каждый в своём шарде со своим процессом оператора и идут параллельно.
Поведение reconcile задаётся полями `spec` виджета (`failTimes`, `panicTimes`, `requeueAfterMs`, `sleepMs`,
`children`, `setLabels`, `remoteNotes`, `marker`, см. `fixtures/e2e-operator/api/widget_types.go`).

Переменные `scripts/e2e.sh`:

- `E2E_PHASES` — какие фазы запускать (`no-auth`, `auth`; по умолчанию обе);
- `E2E_RUN` — фильтр `go test -run`;
- `E2E_OPERATOR_LOGS=1` — печатать логи операторов сразу (по умолчанию — только у упавших тестов);
- `E2E_KNOWN_BUGS=1` — запустить тесты известных багов библиотек. По умолчанию они пропускаются
  (`known bug: ...` в выводе), в этом режиме падают, пока баг не исправлен.

```bash
E2E_PHASES=no-auth E2E_RUN='TestE2E_' make e2e
E2E_KNOWN_BUGS=1 make e2e
```

## Требования

Go (см. `go.work`), Docker и Docker Compose, `openssl`.

## Тестирование controlloop/common

Базовые версии для этого покрытия:

- api: `cf86c1a2d712aabb539a53fb4c335f08776fbee8`;
- controlloop: `799ad7cb4d41c9b581e209fb07ea034de7913a79`.

Они также указаны в `go.mod` тестов и фикстуры. При запуске через `go.work` фактические
исходники выбирает `prepare-deps.sh`: для воспроизводимого прогона задайте SHA явно.
Provider должен поддерживать `ListOpts.Limit/Offset`; текущий `main` поддерживает их.

```bash
API_REF=cf86c1a2d712aabb539a53fb4c335f08776fbee8 \
CONTROLLOOP_REF=799ad7cb4d41c9b581e209fb07ea034de7913a79 \
E2E_RUN=TestE2E_Common make e2e
```

Новые интеграционные тесты имеют build tag `integration`. `make e2e` уже передаёт
`-tags=integration`, поэтому они входят и в полный прогон, и в CI controlloop.
Для ручного запуска против подготовленного стенда нужен тот же tag и переменные
`E2E_*`, которые устанавливает `scripts/e2e.sh`.

Контрактные тесты не требуют Docker. После `make prepare-deps`:

```bash
go -C fixtures/e2e-operator test -race -count=1 \
  -coverpkg=github.com/reconcile-kit/controlloop/common/... ./common
```

### Ресурсы и цепочка уведомления

`E2EConfig` публикует `spec.value` в `status.resolvedValue` вместе с
`status.resolvedVersion`, затем вызывает notifier. Только после успешного уведомления
он отмечает свою версию обработанной. При ошибке сохраняются `DependentsNotified=False`
с reason `NOTIFY_FAILED` и pending-версия, доступная для повторной попытки после рестарта.

`E2EConsumer` читает результат конфигурации и сохраняет `observedValue` и
`observedConfigVersion`. Уведомление не меняет его spec, но повышает `version` через
обычный `Update`, создаёт событие Redis и вызывает повторный reconcile. Успех notifier
не означает, что потребитель уже обработал событие: это проверяется отдельно.

Пример объектов (Group/Kind/Namespace передаются в HTTP path при создании):

```yaml
resource_group: e2e.reconcile-kit.dev
kind: e2e-config
namespace: default
name: config-a
shard_id: source-shard
spec:
  value: v1
  notify:
    mode: structured
    targetShardID: consumer-shard
    labelSelector: "config in (config-a)"
---
resource_group: e2e.reconcile-kit.dev
kind: e2e-consumer
namespace: default
name: consumer-a
shard_id: consumer-shard
labels:
  config: config-a
spec:
  configName: config-a
  enabled: true
  paused: false
```

Для unstructured-сценария используется `mode: unstructured`. Для выбора по имени
вместо `labelSelector` задаётся `name: consumer-a`. В обоих режимах дополнительно
применяются два локальных фильтра: `enabled` и `!paused`. Namespace берётся у Config,
шард — из `targetShardID`. В auth-фазе оператор источника получает права get/list/update
на потребителей целевого шарда, оператор потребителя — get на конфигурации источника.

Успешный сценарий не использует периодический requeue. Тесты сначала дожидаются
обработки v1, затем меняют только Config на v2/v3 и ожидают новое наблюдение Consumer.
Оба ресурса удаляют только свой finalizer; зависимость не означает владение потребителем.

### Контракты и ошибки

- `ListAll`: пустая/короткая страница, ровно 500, 501 и 1000 элементов, сохранение
  фильтров, переопределение входных limit/offset, ошибки и отмена без частичного результата.
- Notifier: чтение всех страниц до обновлений, пустая выборка, AND-фильтры,
  пропуск `NotFound`, остановка на другой ошибке без отката выполненных обновлений,
  возможность повторного уведомления при retry. Порядок обхода map не фиксируется.
- Unstructured: Create/Get/List/Update/UpdateStatus/Delete, выбор Group/Kind при
  конструировании, независимость клиентов, raw JSON и большие целые числа,
  изоляция DeepCopy для spec/status/labels/annotations/finalizers/metadata.
- Реальный state-manager: `Update` при неизменном spec создаёт событие,
  `UpdateStatus` не создаёт; конфликт версий сохраняется как `resource.ConflictError`.

Тест `TestDeepCopy_SlicesAndMapsAreIndependent` теперь выполняется по умолчанию:
на указанной версии api прежняя ошибка shallow copy исправлена.

### Известный дефект: цель удалена между List и Update

`TestE2E_CommonWatchedByDeletedTarget` расширяет существующую регрессию
`TestContract_MissingResourceIs404`: state-manager возвращает HTTP 500 при обновлении
уже удалённого ресурса. Provider превращает это в серверную ошибку, поэтому оба notifier
не получают `resource.NotFoundError` и не могут пропустить исчезнувшую цель.

Тест детерминированно удаляет реальную цель внутри локального predicate селектора:
после завершения List, перед Update. Ожидаемое поведение — успешное уведомление
с пропуском цели. До исправления state-manager тест падает и включается существующим
флагом известных багов:

```bash
API_REF=cf86c1a2d712aabb539a53fb4c335f08776fbee8 \
CONTROLLOOP_REF=799ad7cb4d41c9b581e209fb07ea034de7913a79 \
E2E_KNOWN_BUGS=1 E2E_RUN=TestE2E_CommonWatchedByDeletedTarget make e2e
```

Флаг `E2E_KNOWN_BUGS` также включает прежние регрессии unit-тестов фикстуры;
`E2E_RUN` фильтрует только интеграционный пакет.

Обычные контрактные тесты notifier проверяют корректный пропуск sentinel `NotFound`
без этого флага. Исправления соседних библиотек в этот репозиторий не включены.
