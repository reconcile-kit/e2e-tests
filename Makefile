.PHONY: test e2e prepare-deps work-sync

# Go workspace (go.work): модули e2e, e2e-operator и клоны reconcile-kit в build/deps.
# Без «make prepare-deps» каталогов build/deps/* нет — go work / сборка не сработают.
work-sync:
	@go work sync

# Версии: FRAMEWORK_REF — по умолчанию (ветка / тег / SHA). Переопределения: API_REF,
# CONTROLLOOP_REF, STATE_MANAGER_PROVIDER_REF, REDIS_INFORMER_PROVIDER_REF,
# RUNTIME_MANAGER_REF, STATE_MANAGER_REF.
# PR: значение pr/<номер> в том же поле (например STATE_MANAGER_REF=pr/42).
# Локальный код: путь к каталогу с go.mod или родитель монорепо с подпапкой <имя-репо>/
#   make e2e API_REF=$HOME/ws/api FRAMEWORK_REF=main
#   make e2e API_REF=../api
#   make e2e FRAMEWORK_REF=$HOME/ws/reconcile-monorepo   # ищет …/api/go.mod, …/controlloop/go.mod, …
# Файл versions.env.example; подключение: source versions.env && make e2e
test: e2e

e2e:
	@FRAMEWORK_REF=$(FRAMEWORK_REF) \
	API_REF=$(API_REF) \
	CONTROLLOOP_REF=$(CONTROLLOOP_REF) \
	STATE_MANAGER_PROVIDER_REF=$(STATE_MANAGER_PROVIDER_REF) \
	REDIS_INFORMER_PROVIDER_REF=$(REDIS_INFORMER_PROVIDER_REF) \
	RUNTIME_MANAGER_REF=$(RUNTIME_MANAGER_REF) \
	STATE_MANAGER_REF=$(STATE_MANAGER_REF) \
	./scripts/e2e.sh

prepare-deps:
	@FRAMEWORK_REF=$(FRAMEWORK_REF) \
	API_REF=$(API_REF) \
	CONTROLLOOP_REF=$(CONTROLLOOP_REF) \
	STATE_MANAGER_PROVIDER_REF=$(STATE_MANAGER_PROVIDER_REF) \
	REDIS_INFORMER_PROVIDER_REF=$(REDIS_INFORMER_PROVIDER_REF) \
	RUNTIME_MANAGER_REF=$(RUNTIME_MANAGER_REF) \
	STATE_MANAGER_REF=$(STATE_MANAGER_REF) \
	./scripts/prepare-deps.sh
