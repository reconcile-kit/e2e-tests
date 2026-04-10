.PHONY: test e2e prepare-deps work-sync

# Go workspace (go.work): модули e2e, e2e-operator и клоны reconcile-kit в build/deps.
# Без «make prepare-deps» каталогов build/deps/* нет — go work / сборка не сработают.
work-sync:
	@go work sync

# Версии зависимостей: API_REF, CONTROLLOOP_REF, STATE_MANAGER_PROVIDER_REF,
# REDIS_INFORMER_PROVIDER_REF, RUNTIME_MANAGER_REF, STATE_MANAGER_REF.
# Не заданный ref → для этого репо main. Форматы: ветка/тег/SHA, pr/<N>, путь к go.mod или монорепо.
# Файл versions.env.example; подключение: source versions.env && make e2e
test: e2e

e2e:
	@API_REF=$(API_REF) \
	CONTROLLOOP_REF=$(CONTROLLOOP_REF) \
	STATE_MANAGER_PROVIDER_REF=$(STATE_MANAGER_PROVIDER_REF) \
	REDIS_INFORMER_PROVIDER_REF=$(REDIS_INFORMER_PROVIDER_REF) \
	RUNTIME_MANAGER_REF=$(RUNTIME_MANAGER_REF) \
	STATE_MANAGER_REF=$(STATE_MANAGER_REF) \
	./scripts/e2e.sh

prepare-deps:
	@API_REF=$(API_REF) \
	CONTROLLOOP_REF=$(CONTROLLOOP_REF) \
	STATE_MANAGER_PROVIDER_REF=$(STATE_MANAGER_PROVIDER_REF) \
	REDIS_INFORMER_PROVIDER_REF=$(REDIS_INFORMER_PROVIDER_REF) \
	RUNTIME_MANAGER_REF=$(RUNTIME_MANAGER_REF) \
	STATE_MANAGER_REF=$(STATE_MANAGER_REF) \
	./scripts/prepare-deps.sh
