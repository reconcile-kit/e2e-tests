package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Сценарии полного контура с запущенным оператором. Каждый тест — в своём шарде со своим
// процессом оператора, поэтому тесты параллельны.

const operatorFinalizer = "e2e.reconcile-kit.dev/finalizer"

// TestE2E_CreateWhileOperatorRunning: ресурс создаётся при уже работающем операторе и доходит
// до Ready через событие Redis (не через ListPending при старте). Событие подтверждается
// и удаляется из стрима.
func TestE2E_CreateWhileOperatorRunning(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-event")
	createWidget(t, cl, shard, name, map[string]any{"marker": "v1"})
	_, st := waitSynced(t, cl, name)
	require.Equal(t, "v1", st.ObservedMarker)
	require.Equal(t, 2, st.Reconciles)

	rdb := redisClient(t)
	require.NoError(t, waitUntil(10*time.Second, pollStep, func() (bool, error) {
		return pendingCount(t, rdb, shard) == 0 && len(eventsFor(streamEvents(t, rdb, shard), name)) == 0, nil
	}), "event must be acked and deleted from the stream")
}

// TestE2E_SpecUpdateTriggersReconcile: PUT spec при работающем операторе → version+1 → событие →
// reconcile новой spec; в итоге current_version == version и observedMarker из новой spec.
func TestE2E_SpecUpdateTriggersReconcile(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-upd")
	createWidget(t, cl, shard, name, map[string]any{"marker": "v1"})
	r, _ := waitSynced(t, cl, name)

	for _, marker := range []string{"v2", "v3"} {
		_, code, err := cl.UpdateResource(name, UpdateBody{
			ShardID: shard, Spec: mustJSON(map[string]any{"marker": marker}),
			Finalizers: r.Finalizers, Version: intPtr(r.Version),
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		r = waitWidget(t, cl, name, func(r *ResourceDTO, st widgetStatus) bool {
			return synced(r, st) && st.ObservedMarker == marker
		})
	}
	require.Equal(t, 3, r.Version)
	require.Equal(t, 3, r.CurrentVersion)
}

// TestE2E_SpecUpdateDuringReconcile_Conflict: spec меняется, пока идёт проход reconcile
// (sleepMs). UpdateStatus оператора получает 409, проход повторяется уже с новой spec, и
// итоговый статус соответствует последней версии.
func TestE2E_SpecUpdateDuringReconcile_Conflict(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-conflict")
	createWidget(t, cl, shard, name, map[string]any{"marker": "v1", "sleepMs": 2000})
	time.Sleep(700 * time.Millisecond) // первый проход спит внутри reconcile
	_, code, err := cl.UpdateResource(name, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "v2", "sleepMs": 300})})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	r := waitWidget(t, cl, name, func(r *ResourceDTO, st widgetStatus) bool {
		return synced(r, st) && st.ObservedMarker == "v2"
	})
	require.Equal(t, 2, r.Version)
	require.Contains(t, strings.ToLower(op.Logs()), "conflict", "the stale UpdateStatus must hit a version conflict")
}

// TestE2E_DeleteWithoutFinalizerWhileRunning: ресурс без финализатора (gadget) удаляется сразу,
// событие delete убирает его из памяти оператора — пересозданный с тем же именем ресурс
// обрабатывается заново.
func TestE2E_DeleteWithoutFinalizerWhileRunning(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})
	rdb := redisClient(t)

	name := uniqueName("g-del")
	_, err := cl.Create(CreateOpts{Kind: gadgetKind, ShardID: shard, Name: name, Spec: mustJSON(map[string]any{"marker": "first"})})
	require.NoError(t, err)
	waitResource(t, cl, gadgetKind, name, readyTimeout, func(r *ResourceDTO, st widgetStatus) bool {
		return st.ready() && st.ObservedMarker == "first"
	})

	code, b, err := cl.Do(http.MethodDelete, cl.kindResourcePath(gadgetKind, name), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, code, string(b))
	_, code, err = cl.GetKind(gadgetKind, name)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, code, "resource without finalizers is deleted immediately")
	require.NoError(t, waitUntil(10*time.Second, pollStep, func() (bool, error) {
		return pendingCount(t, rdb, shard) == 0 && len(eventsFor(streamEvents(t, rdb, shard), name)) == 0, nil
	}), "delete event must be consumed")

	_, err = cl.Create(CreateOpts{Kind: gadgetKind, ShardID: shard, Name: name, Spec: mustJSON(map[string]any{"marker": "second"})})
	require.NoError(t, err)
	r := waitResource(t, cl, gadgetKind, name, readyTimeout, func(r *ResourceDTO, st widgetStatus) bool {
		return st.ready() && st.ObservedMarker == "second"
	})
	st, err := parseWidgetStatus(r.Status)
	require.NoError(t, err)
	require.Equal(t, 1, st.Reconciles, "recreated resource starts from scratch")
}

// TestE2E_ReconcileErrorRetries: reconcile возвращает ошибку failTimes раз — controlloop
// повторяет с backoff, затем ресурс доходит до Ready. Пока идут ошибки, Ready=False.
func TestE2E_ReconcileErrorRetries(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-fail")
	createWidget(t, cl, shard, name, map[string]any{"failTimes": 3})
	_, st := waitSynced(t, cl, name)
	require.Equal(t, 3, st.Failures)
	require.Equal(t, 5, st.Reconciles, "finalizer + 3 failures + success")
}

// TestE2E_ReconcileErrorKeepsNotReady: пока reconcile падает, ресурс не помечен обработанным
// (current_version не двигается, Synced не True), а оператор продолжает работать.
// Ready проверяется отдельно — TestE2E_FailingResourceNotReady (известный баг).
func TestE2E_ReconcileErrorKeepsNotReady(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-failing")
	createWidget(t, cl, shard, name, map[string]any{"failTimes": 1000})
	waitWidget(t, cl, name, func(r *ResourceDTO, st widgetStatus) bool { return st.Failures >= 2 })

	r, st := getWidget(t, cl, name)
	require.NotEqual(t, "True", st.cond("E2EWidgetSynced"))
	require.Equal(t, 0, r.CurrentVersion)
	require.False(t, op.Exited())

	// Другие ресурсы шарда обрабатываются, несмотря на падающий.
	ok := uniqueName("w-ok")
	createWidget(t, cl, shard, ok, nil)
	waitSynced(t, cl, ok)
}

// TestE2E_FailingResourceNotReady: ресурс, reconcile которого падает, не должен быть Ready.
//
// Известный баг api/conditions + типового defer оператора: после прохода с финализатором
// в conditions только Ready=False; на проходе с ошибкой SyncReady не находит False-условий
// (Ready пропускается) и ставит Ready=True.
func TestE2E_FailingResourceNotReady(t *testing.T) {
	t.Parallel()
	knownBug(t, "SyncReady marks a resource Ready after a failed reconcile")
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-failing-ready")
	createWidget(t, cl, shard, name, map[string]any{"failTimes": 1000})
	waitWidget(t, cl, name, func(_ *ResourceDTO, st widgetStatus) bool { return st.Failures >= 2 })
	_, st := getWidget(t, cl, name)
	require.False(t, st.ready(), "resource with a failing reconcile must not be Ready")
}

// TestE2E_PanickedResourceStaysPending: проход, завершившийся паникой, не должен помечать
// ресурс обработанным — иначе после рестарта оператора ListPending его не вернёт.
//
// Известный баг типового defer оператора (исходная фикстура / operator-layout): при панике
// result и reterr нулевые, defer вызывает SetCurrentVersion + UpdateStatus.
func TestE2E_PanickedResourceStaysPending(t *testing.T) {
	t.Parallel()
	knownBug(t, "reconcile defer marks a panicked pass as observed (current_version = version)")
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-panic-pending")
	createWidget(t, cl, shard, name, map[string]any{"panicTimes": 1000})
	waitWidget(t, cl, name, func(_ *ResourceDTO, st widgetStatus) bool { return st.Panics >= 1 })
	r, _ := getWidget(t, cl, name)
	require.Equal(t, 0, r.CurrentVersion, "panicked pass must not mark the version as observed")
	items, err := cl.List(map[string]string{"pending": "true", "shard_id": shard})
	require.NoError(t, err)
	require.Contains(t, Names(items), name)
}

// TestE2E_ReconcilePanicRecovered: паника в reconcile перехватывается controlloop, процесс
// не падает, ресурс повторяется и доходит до Ready.
func TestE2E_ReconcilePanicRecovered(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-panic")
	createWidget(t, cl, shard, name, map[string]any{"panicTimes": 2})
	_, st := waitSynced(t, cl, name)
	require.Equal(t, 2, st.Panics)
	require.False(t, op.Exited())
	require.Contains(t, op.Logs(), "recovered from panic")
}

// TestE2E_RequeueAfter: Result{RequeueAfter} откладывает следующий проход не меньше чем на
// заданную задержку.
func TestE2E_RequeueAfter(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	const delay = 1500 * time.Millisecond
	name := uniqueName("w-after")
	createWidget(t, cl, shard, name, map[string]any{"requeueAfterMs": delay.Milliseconds(), "requeueAfterTimes": 2})
	_, st := waitSynced(t, cl, name)
	require.Equal(t, 2, st.RequeueAfters)
	// Проходы: финализатор, RequeueAfter #1, RequeueAfter #2, финальный.
	require.Len(t, st.PassTimes, 4)
	for i := 2; i < 4; i++ {
		gap := st.PassTimes[i].Sub(st.PassTimes[i-1])
		require.GreaterOrEqual(t, gap, delay-50*time.Millisecond, "pass %d came after %s", i, gap)
	}
}

// TestE2E_PendingClearedAfterSync: новый ресурс в pending-выборке, после обработки — нет.
func TestE2E_PendingClearedAfterSync(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("w-pending")
	createWidget(t, cl, shard, name, nil)
	pending := map[string]string{"pending": "true", "resource_group": widgetGroup, "kind": widgetKind, "shard_id": shard}

	items, err := cl.List(pending)
	require.NoError(t, err)
	require.Equal(t, []string{name}, Names(items))

	startOperator(t, operatorOpts{Shard: shard})
	waitSynced(t, cl, name)
	items, err = cl.List(pending)
	require.NoError(t, err)
	require.Empty(t, items)
}

// TestE2E_OperatorRestartResumes: изменения, сделанные пока оператор остановлен (новая spec,
// новый ресурс, удаление ресурса с финализатором), подхватываются после рестарта через Init.
func TestE2E_OperatorRestartResumes(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})

	updated := uniqueName("w-restart-upd")
	deleted := uniqueName("w-restart-del")
	createWidget(t, cl, shard, updated, map[string]any{"marker": "v1"})
	createWidget(t, cl, shard, deleted, nil)
	r, _ := waitSynced(t, cl, updated)
	waitSynced(t, cl, deleted)

	code, graceful := op.Stop(operatorStopTimeout)
	require.True(t, graceful, "operator must stop on SIGTERM")
	require.Equal(t, 0, code)

	_, httpCode, err := cl.UpdateResource(updated, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "v2"}), Finalizers: r.Finalizers})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, httpCode)
	created := uniqueName("w-restart-new")
	createWidget(t, cl, shard, created, nil)
	require.NoError(t, cl.DeleteResource(deleted))
	d, _ := getWidget(t, cl, deleted)
	require.NotNil(t, d.DeletionTimestamp, "finalizer keeps the resource until the operator is back")

	startOperator(t, operatorOpts{Shard: shard})
	waitWidget(t, cl, updated, func(r *ResourceDTO, st widgetStatus) bool { return synced(r, st) && st.ObservedMarker == "v2" })
	waitSynced(t, cl, created)
	waitGone(t, cl, deleted)
}

// TestE2E_ForeignFinalizerBlocksDeletion: оператор снимает при удалении только свой финализатор;
// ресурс живёт, пока владелец чужого финализатора не снимет его.
func TestE2E_ForeignFinalizerBlocksDeletion(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-foreign")
	_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Spec: mustJSON(map[string]any{}), Finalizers: []string{"e2e.test/hold"}})
	require.NoError(t, err)
	r, _ := waitSynced(t, cl, name)
	require.ElementsMatch(t, []string{"e2e.test/hold", operatorFinalizer}, r.Finalizers)

	require.NoError(t, cl.DeleteResource(name))
	r = waitWidget(t, cl, name, func(r *ResourceDTO, _ widgetStatus) bool {
		return r.DeletionTimestamp != nil && !contains(r.Finalizers, operatorFinalizer)
	})
	require.Equal(t, []string{"e2e.test/hold"}, r.Finalizers)

	_, code, err := cl.UpdateResource(name, UpdateBody{ShardID: shard})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	waitGone(t, cl, name)
}

// TestE2E_ManyResources: десятки ресурсов, созданных параллельно при работающем операторе,
// обрабатываются ровно по два прохода (без потерь и дублей), по одному reconcile за раз
// (воркер по умолчанию один).
func TestE2E_ManyResources(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	const n = 60
	names := make([]string, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range names {
		names[i] = uniqueName(fmt.Sprintf("w-many%02d", i))
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Spec: mustJSON(map[string]any{})})
			errs <- err
		}(names[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	for _, name := range names {
		_, st := waitSynced(t, cl, name)
		require.Equal(t, 2, st.Reconciles, name)
		require.Equal(t, 1, st.MaxParallel, "single worker must not run reconciles in parallel")
	}
}

// TestE2E_InitPagination: больше 250 pending-ресурсов до старта оператора — ListPending
// читает их страницами по 100, и все доходят до Ready.
func TestE2E_InitPagination(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	const n = 250
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("w-init-%03d-%s", i, randHex(2))
		createWidget(t, cl, shard, names[i], nil)
	}

	startOperator(t, operatorOpts{Shard: shard})
	require.NoError(t, waitUntil(120*time.Second, time.Second, func() (bool, error) {
		items, err := cl.List(map[string]string{"pending": "true", "resource_group": widgetGroup, "kind": widgetKind, "shard_id": shard})
		return err == nil && len(items) == 0, err
	}), "all resources must leave pending")
	all, err := cl.List(map[string]string{"resource_group": widgetGroup, "kind": widgetKind, "shard_id": shard})
	require.NoError(t, err)
	require.Len(t, all, n)
	for _, r := range all {
		st, err := parseWidgetStatus(r.Status)
		require.NoError(t, err)
		require.True(t, synced(&r, st), r.Name)
	}
}

// TestE2E_ConcurrentWorkers: с WithConcurrentWorkers(4) разные ключи обрабатываются параллельно,
// но один и тот же ключ — никогда.
func TestE2E_ConcurrentWorkers(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	const n = 12
	names := make([]string, n)
	for i := range names {
		names[i] = uniqueName(fmt.Sprintf("w-par%02d", i))
		createWidget(t, cl, shard, names[i], map[string]any{"sleepMs": 400, "requeueExtra": 2})
	}
	startOperator(t, operatorOpts{Shard: shard, Workers: 4})

	maxParallel := 0
	for _, name := range names {
		_, st := waitSynced(t, cl, name)
		require.False(t, st.SameKeyOverlap, "%s was reconciled concurrently with itself", name)
		require.LessOrEqual(t, st.MaxParallel, 4)
		maxParallel = max(maxParallel, st.MaxParallel)
	}
	require.Greater(t, maxParallel, 1, "workers must run in parallel")
}

// TestE2E_ShardIsolation: оператор обрабатывает только свой шард; ресурс шарда без оператора
// не трогается (нет финализатора, current_version=0).
func TestE2E_ShardIsolation(t *testing.T) {
	t.Parallel()
	shardA, cl := setupShard(t)
	shardB, shardC := newShard(), newShard()
	startOperator(t, operatorOpts{Shard: shardA})
	startOperator(t, operatorOpts{Shard: shardB})

	a, b, c := uniqueName("w-shard-a"), uniqueName("w-shard-b"), uniqueName("w-shard-c")
	createWidget(t, cl, shardA, a, nil)
	createWidget(t, cl, shardB, b, nil)
	createWidget(t, cl, shardC, c, nil)
	ra, _ := waitSynced(t, cl, a)
	rb, _ := waitSynced(t, cl, b)
	require.Equal(t, shardA, ra.ShardID)
	require.Equal(t, shardB, rb.ShardID)

	requireNever(t, 3*time.Second, func() bool {
		r, _ := getWidget(t, cl, c)
		return r.CurrentVersion != 0 || len(r.Finalizers) != 0 || hasStatus(r)
	}, "resource of a shard without operator must stay untouched")
}

// TestE2E_MultipleKinds: два контроллера в одном менеджере — события маршрутизируются по
// GroupKind, каждый тип обрабатывает свой reconciler.
func TestE2E_MultipleKinds(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("mk")
	_, err := cl.Create(CreateOpts{Kind: gadgetKind, ShardID: shard, Name: name, Spec: mustJSON(map[string]any{"marker": "g"})})
	require.NoError(t, err)
	createWidget(t, cl, shard, name, map[string]any{"marker": "w"}) // то же имя, другой kind

	g := waitResource(t, cl, gadgetKind, name, readyTimeout, func(r *ResourceDTO, st widgetStatus) bool {
		return st.ready() && r.Version == r.CurrentVersion
	})
	gst, err := parseWidgetStatus(g.Status)
	require.NoError(t, err)
	require.Equal(t, "g", gst.ObservedMarker)
	require.Empty(t, g.Finalizers, "gadget controller does not add finalizers")
	require.Equal(t, "True", gst.cond("E2EGadgetSynced"))

	_, wst := waitSynced(t, cl, name)
	require.Equal(t, "w", wst.ObservedMarker)
}

// TestE2E_ChildResourcesViaStorage: reconcile создаёт дочерние ресурсы через Storage.Create
// (StorageController → state-manager), они сами проходят reconcile, родитель ждёт их Ready.
func TestE2E_ChildResourcesViaStorage(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	parent := uniqueName("w-parent")
	children := []string{parent + "-c1", parent + "-c2"}
	createWidget(t, cl, shard, parent, map[string]any{"children": children})

	_, st := waitSynced(t, cl, parent)
	require.ElementsMatch(t, children, st.ChildrenReady)
	for _, c := range children {
		r, _ := waitSynced(t, cl, c)
		require.Equal(t, shard, r.ShardID)
		require.Equal(t, parent, r.Labels["e2e-parent"])
	}
	items, err := cl.List(map[string]string{"shard_id": shard, "label_selector": "e2e-parent in (" + parent + ")"})
	require.NoError(t, err)
	require.ElementsMatch(t, children, Names(items))
}

// TestE2E_RemoteClient: тип без контроллера (SetRemoteClient) — reconcile создаёт его через
// RemoteClient, ресурс появляется в state-manager и остаётся pending (его никто не обрабатывает).
func TestE2E_RemoteClient(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	owner := uniqueName("w-remote")
	note := owner + "-note"
	createWidget(t, cl, shard, owner, map[string]any{"remoteNotes": []string{note}})
	_, st := waitSynced(t, cl, owner)
	require.Equal(t, []string{note}, st.RemoteNotes)

	r, code, err := cl.GetKind(remoteNoteKind, note)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, fmt.Sprintf(`{"owner":%q}`, owner), string(r.Spec))
	require.Equal(t, shard, r.ShardID)
	require.Equal(t, 0, r.CurrentVersion, "nobody reconciles remote-only kinds")
}

// TestE2E_LabelsSetByController: лейблы, выставленные reconcile, сохраняются через UpdateStatus
// вместе с клиентскими и находятся селектором.
func TestE2E_LabelsSetByController(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})

	name := uniqueName("w-labels")
	_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Labels: map[string]string{"team": "a"},
		Spec: mustJSON(map[string]any{"setLabels": map[string]string{"managed": "yes"}})})
	require.NoError(t, err)
	r, _ := waitSynced(t, cl, name)
	require.Equal(t, map[string]string{"team": "a", "managed": "yes"}, r.Labels)

	items, err := cl.List(map[string]string{"shard_id": shard, "label_selector": "managed in (yes),team in (a)"})
	require.NoError(t, err)
	require.Equal(t, []string{name}, Names(items))
}

// TestE2E_GracefulShutdownIdle: SIGTERM → процесс завершается сам с кодом 0.
func TestE2E_GracefulShutdownIdle(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})
	name := uniqueName("w-stop")
	createWidget(t, cl, shard, name, nil)
	waitSynced(t, cl, name)

	code, graceful := op.Stop(10 * time.Second)
	require.True(t, graceful, "operator must exit on SIGTERM without SIGKILL")
	require.Equal(t, 0, code)
	require.Contains(t, op.Logs(), "stopped")
}

// TestE2E_GracefulShutdownWithDelayedRequeue: SIGTERM, когда в очереди есть ресурс с отложенным
// RequeueAfter, — процесс всё равно завершается за разумное время.
//
// Известный баг controlloop: при остановке элементы из existedItems возвращаются в очередь, а
// reconcile снова делает RequeueAfter — очередь никогда не пустеет и Stop не возвращается.
func TestE2E_GracefulShutdownWithDelayedRequeue(t *testing.T) {
	t.Parallel()
	knownBug(t, "controlloop Stop hangs while an item keeps returning RequeueAfter")
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})
	name := uniqueName("w-stop-after")
	createWidget(t, cl, shard, name, map[string]any{"requeueAfterMs": 60000, "requeueAfterTimes": 100})
	waitWidget(t, cl, name, func(_ *ResourceDTO, st widgetStatus) bool { return st.RequeueAfters >= 1 })

	code, graceful := op.Stop(15 * time.Second)
	require.True(t, graceful, "operator must exit on SIGTERM while an item waits for RequeueAfter")
	require.Equal(t, 0, code)
}

// TestE2E_GracefulShutdownWithFailingResource: SIGTERM, когда ресурс в backoff после ошибок.
//
// Известный баг controlloop (тот же механизм, что с RequeueAfter): Stop не возвращается.
func TestE2E_GracefulShutdownWithFailingResource(t *testing.T) {
	t.Parallel()
	knownBug(t, "controlloop Stop hangs while an item keeps failing")
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})
	name := uniqueName("w-stop-fail")
	createWidget(t, cl, shard, name, map[string]any{"failTimes": 100000})
	waitWidget(t, cl, name, func(_ *ResourceDTO, st widgetStatus) bool { return st.Failures >= 2 })

	code, graceful := op.Stop(15 * time.Second)
	require.True(t, graceful, "operator must exit on SIGTERM while an item is failing")
	require.Equal(t, 0, code)
}

// TestE2E_OperatorConfigErrors: без обязательных переменных или с недоступными Redis /
// state-manager оператор завершается с ненулевым кодом, а не висит.
func TestE2E_OperatorConfigErrors(t *testing.T) {
	t.Parallel()
	skipIfNoEnv(t)
	cases := []struct {
		name string
		o    operatorOpts
		log  string
	}{
		{"no storage url", operatorOpts{Unset: []string{"E2E_STORAGE_URL"}}, "E2E_STORAGE_URL is required"},
		{"no informer url", operatorOpts{Unset: []string{"E2E_INFORMER_URL"}}, "E2E_INFORMER_URL is required"},
		{"redis unreachable", operatorOpts{InformerURL: "127.0.0.1:1"}, "manager run"},
		{"state-manager unreachable", operatorOpts{StorageURL: "http://127.0.0.1:1"}, "manager run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.o.NoWaitReady = true
			op := startOperator(t, c.o)
			code, ok := op.WaitExit(45 * time.Second)
			require.True(t, ok, "operator must exit; logs:\n%s", op.Logs())
			require.NotEqual(t, 0, code)
			require.Contains(t, op.Logs(), c.log)
		})
	}
}
