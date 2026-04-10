#!/usr/bin/env bash
# Клонирует репозитории reconcile-kit в build/deps (для go.work и образа state-manager).
#
# FRAMEWORK_REF — по умолчанию: ветка, тег, SHA, или каталог с исходниками (см. ниже).
# Переопределения: API_REF, CONTROLLOOP_REF, STATE_MANAGER_PROVIDER_REF,
# REDIS_INFORMER_PROVIDER_REF, RUNTIME_MANAGER_REF, STATE_MANAGER_REF.
#
# Режимы значения ref:
#   • pr/<N>     — GitHub Pull Request (refs/pull/N/head)
#   • локальный каталог — абсолютный или относительно корня e2e-tests (./…); должен быть go.mod.
#     Можно указать корень модуля …/api или родитель с подкаталогом <имя-репо>/go.mod
#   • иначе      — git branch / tag / SHA (как раньше)
#
# Локальный путь не трогаем git-командами (только симлинк в build/deps/<repo>).
#
# Примеры:
#   API_REF=$HOME/src/api ./scripts/prepare-deps.sh
#   API_REF=./vendor/reconcile-api FRAMEWORK_REF=main ./scripts/prepare-deps.sh
#   FRAMEWORK_REF=main STATE_MANAGER_REF=pr/42 ./scripts/prepare-deps.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPS="${ROOT}/build/deps"
FRAMEWORK_REF="${FRAMEWORK_REF:-main}"
mkdir -p "${DEPS}"

ref_for_repo() {
	local name="$1"
	local override=""
	case "${name}" in
	api) override="${API_REF:-}" ;;
	controlloop) override="${CONTROLLOOP_REF:-}" ;;
	state-manager-provider) override="${STATE_MANAGER_PROVIDER_REF:-}" ;;
	redis-informer-provider) override="${REDIS_INFORMER_PROVIDER_REF:-}" ;;
	runtime-manager) override="${RUNTIME_MANAGER_REF:-}" ;;
	state-manager) override="${STATE_MANAGER_REF:-}" ;;
	esac
	if [[ -n "${override}" ]]; then
		echo "${override}"
	else
		echo "${FRAMEWORK_REF}"
	fi
}

ref_is_pr() {
	[[ "${1:-}" =~ ^pr/[0-9]+$ ]]
}

pr_from_ref() {
	echo "${1#pr/}"
}

# Нормализация пути; относительные пути — от корня репозитория e2e-tests.
normalize_filesystem_path() {
	local p="$1"
	case "${p}" in
	file://*) p="${p#file://}" ;;
	esac
	case "${p}" in
	~/*) p="${HOME}/${p#~/}" ;;
	~/) p="${HOME}/" ;;
	esac
	if [[ "${p}" != /* ]]; then
		p="${ROOT}/${p}"
	fi
	echo "${p}"
}

# Если ref задаёт локальный модуль: сам каталог с go.mod или <ref>/<имя-репо>/go.mod (монорепо).
expand_local_module_path() {
	local ref="$1"
	local name="$2"
	local base
	base="$(normalize_filesystem_path "${ref}")"
	if [[ -f "${base}/go.mod" ]]; then
		(cd "${base}" && pwd)
		return 0
	fi
	if [[ -f "${base}/${name}/go.mod" ]]; then
		(cd "${base}/${name}" && pwd)
		return 0
	fi
	return 1
}

remove_target_for_git() {
	local target="$1"
	if [[ -L "${target}" ]] || [[ -d "${target}" ]]; then
		rm -rf "${target}"
	fi
}

link_local_module() {
	local name="$1"
	local abs="$2"
	local target="${DEPS}/${name}"
	remove_target_for_git "${target}"
	ln -sfn "${abs}" "${target}"
	echo "prepare-deps: ${name} (local -> ${abs})"
}

clone_one() {
	local name="$1"
	local ref
	ref="$(ref_for_repo "${name}")"
	local url="https://github.com/reconcile-kit/${name}.git"
	local target="${DEPS}/${name}"

	if ref_is_pr "${ref}"; then
		if [[ -L "${target}" ]]; then
			remove_target_for_git "${target}"
		fi
		local pr
		pr="$(pr_from_ref "${ref}")"
		local branch="pr-${pr}"
		echo "prepare-deps: ${name} @ PR #${pr}"
		if [[ ! -d "${target}/.git" ]]; then
			git clone "${url}" "${target}"
		fi
		git -C "${target}" fetch --unshallow 2>/dev/null || true
		if ! git -C "${target}" fetch origin "pull/${pr}/head"; then
			echo "prepare-deps: fetch PR #${pr} не удалился для ${name}; при необходимости удалите ${target} и повторите" >&2
			exit 1
		fi
		git -C "${target}" checkout -q -B "${branch}" FETCH_HEAD
		return
	fi

	local local_abs
	if local_abs="$(expand_local_module_path "${ref}" "${name}")"; then
		link_local_module "${name}" "${local_abs}"
		return
	fi

	# Дальше — обычный git: если раньше был симлинк на локальный код — убираем и клонируем.
	if [[ -L "${target}" ]]; then
		echo "prepare-deps: ${name} (replace symlink with git ref ${ref})"
		remove_target_for_git "${target}"
	fi

	if [[ -d "${target}/.git" ]]; then
		echo "prepare-deps: ${name} (existing -> ${ref})"
		git -C "${target}" fetch --depth 1 origin "${ref}" 2>/dev/null || true
		git -C "${target}" checkout -q "${ref}" 2>/dev/null || git -C "${target}" checkout -q FETCH_HEAD
		return
	fi
	echo "prepare-deps: ${name} @ ${ref}"
	if ! git clone --depth 1 --branch "${ref}" "${url}" "${target}" 2>/dev/null; then
		git clone "${url}" "${target}"
		git -C "${target}" checkout -q "${ref}"
	fi
}

for repo in api controlloop state-manager-provider redis-informer-provider runtime-manager state-manager; do
	clone_one "${repo}"
done

echo "prepare-deps: ok -> ${DEPS} (FRAMEWORK_REF=${FRAMEWORK_REF})"
