// Package e2e — интеграционные тесты полного контура: state-manager (Postgres + Redis),
// Redis Streams и тестовый оператор (fixtures/e2e-operator). Запуск: make e2e или ./scripts/e2e.sh.
//
// Тесты этого файла не зависят от режима state-manager: scripts/e2e.sh прогоняет их и без
// авторизации, и с авторизацией (E2E_AUTH=1) — тогда клиент и оператор ходят с JWT.
package e2e

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// skipIfNoEnv пропускает тесты без поднятого стенда (локальный go test без Docker).
func skipIfNoEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_STORAGE_URL") == "" {
		t.Skip("set E2E_STORAGE_URL (run ./scripts/e2e.sh)")
	}
}

// setupE2E возвращает shard (по умолчанию e2e-shard-1) и клиент state-manager.
func setupE2E(t *testing.T) (shard string, cl *SMClient) {
	t.Helper()
	skipIfNoEnv(t)
	base := os.Getenv("E2E_STORAGE_URL")
	shard = os.Getenv("E2E_SHARD_ID")
	if shard == "" {
		shard = "e2e-shard-1"
	}
	var err error
	cl, err = NewSMClient(base)
	require.NoError(t, err)
	if authEnabled() {
		// Права клиента — из БД по sub (fixtures/auth/seed.sql).
		cl = cl.WithToken(signToken(t, tokenOpts{Subject: authClientSubject}))
	}
	return shard, cl
}

// setupShard — как setupE2E, но с отдельным шардом на тест.
func setupShard(t *testing.T) (shard string, cl *SMClient) {
	t.Helper()
	_, cl = setupE2E(t)
	return newShard(), cl
}

// startTestOperator запускает бинарник e2e-operator с типичным окружением, ждёт готовности
// и регистрирует остановку в cleanup. Если logLevel непустой — выставляется E2E_LOG_LEVEL.
func startTestOperator(t *testing.T, shard string, logLevel string) *operatorProc {
	t.Helper()
	return startOperator(t, operatorOpts{Shard: shard, LogLevel: logLevel})
}

// TestE2E_CreateBecomesReady проверяет «счастливый путь» для ресурса, созданного до старта
// оператора: Init (ListPending) → reconcile → финализатор → Ready. Ожидаем Ready=True и
// E2EWidgetSynced=True, финализатор оператора, version == current_version и ровно два прохода
// (добавление финализатора + основной).
func TestE2E_CreateBecomesReady(t *testing.T) {
	shard, cl := setupE2E(t)
	name := uniqueName("widget-ready")
	created := createWidget(t, cl, shard, name, map[string]any{"marker": "v1"})
	require.Equal(t, 1, created.Version)
	require.Equal(t, 0, created.CurrentVersion, "new resource is pending")

	startTestOperator(t, shard, "4")

	r, st := waitSynced(t, cl, name)
	require.Equal(t, r.Version, r.CurrentVersion, "version should match after sync")
	require.Contains(t, r.Finalizers, "e2e.reconcile-kit.dev/finalizer")
	require.Equal(t, "v1", st.ObservedMarker)
	require.Equal(t, 2, st.Reconciles, "finalizer pass + main pass")
	require.Equal(t, shard, r.ShardID)
}

// TestE2E_RequeueExtraThenReady проверяет несколько проходов reconcile с Requeue:
// в spec задано requeueExtra=2 — reconciler увеличивает reconcilePasses и возвращает Requeue,
// пока не выполнит нужное число дополнительных проходов, затем выставляет Ready.
// Успех: Ready=True, reconcilePasses == 2 и ровно 4 прохода (финализатор + 2 requeue + основной).
func TestE2E_RequeueExtraThenReady(t *testing.T) {
	shard, cl := setupE2E(t)
	name := uniqueName("widget-req")
	createWidget(t, cl, shard, name, map[string]any{"requeueExtra": 2})

	startTestOperator(t, shard, "")

	_, st := waitSynced(t, cl, name)
	require.Equal(t, 2, st.ReconcilePasses)
	require.Equal(t, 4, st.Reconciles)
}

// TestE2E_DeleteWithFinalizer проверяет удаление при наличии финализатора (operator добавляет его):
// 1) ресурс доходит до Ready; 2) клиент вызывает DELETE — state-manager выставляет deletion_timestamp;
// 3) reconcile delete снимает finalizers через UpdateStatus; 4) при пустых finalizers state-manager
// удаляет строку — GET возвращает 404 (второй HTTP DELETE не вызывается).
func TestE2E_DeleteWithFinalizer(t *testing.T) {
	shard, cl := setupE2E(t)
	name := uniqueName("widget-del")
	createWidget(t, cl, shard, name, nil)

	startTestOperator(t, shard, "")

	waitSynced(t, cl, name)
	require.NoError(t, cl.DeleteResource(name))
	waitGone(t, cl, name)
}

// TestE2E_LabelsListInReconcile проверяет лейблы end-to-end: несколько ресурсов с разными labels,
// ресурс-агрегатор с spec.labelTestQueries — в reconcile вызывается Storage.List с label selectors
// (как в state-manager HTTP). В status записываются результаты; дополнительно проверяется HTTP List.
func TestE2E_LabelsListInReconcile(t *testing.T) {
	shard, cl := setupShard(t)
	n1 := uniqueName("lbl-a1")
	n2 := uniqueName("lbl-a2")
	n3 := uniqueName("lbl-b1")
	nq := uniqueName("lbl-q")

	_, err := cl.CreateResource(shard, n1, json.RawMessage(`{}`), map[string]string{"team": "alpha", "env": "shared"})
	require.NoError(t, err)
	_, err = cl.CreateResource(shard, n2, json.RawMessage(`{}`), map[string]string{"team": "alpha", "env": "prod"})
	require.NoError(t, err)
	_, err = cl.CreateResource(shard, n3, json.RawMessage(`{}`), map[string]string{"team": "beta", "env": "shared"})
	require.NoError(t, err)

	spec := json.RawMessage(`{"labelTestQueries":["team in (alpha)","env in (shared)","team in (alpha),env in (shared)"]}`)
	_, err = cl.CreateResource(shard, nq, spec, map[string]string{"team": "query", "env": "coord"})
	require.NoError(t, err)

	startTestOperator(t, shard, "")

	_, st := waitSynced(t, cl, nq)
	require.Len(t, st.LabelQueryResults, 3)

	namesOf := func(sel string) []string {
		for _, q := range st.LabelQueryResults {
			if q.Selector == sel {
				require.Equal(t, len(q.Names), q.Count, sel)
				return q.Names
			}
		}
		t.Fatalf("selector %q not found", sel)
		return nil
	}
	require.ElementsMatch(t, []string{n1, n2}, namesOf("team in (alpha)"), "team=alpha: a1+a2")
	require.ElementsMatch(t, []string{n1, n3}, namesOf("env in (shared)"), "env=shared: a1+b1")
	require.ElementsMatch(t, []string{n1}, namesOf("team in (alpha),env in (shared)"), "intersection: a1")

	listAlpha, err := cl.ListResources(shard, "team in (alpha)")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{n1, n2}, Names(listAlpha))
}
