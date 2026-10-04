package controllers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/pkg/logger"
	"github.com/stretchr/testify/require"
)

// Unit-тесты reconcile на in-memory хранилище controlloop (без state-manager и Redis).

type widgetEnv struct {
	r       *WidgetReconciler[*api.E2EWidget]
	widgets *cl.MemoryStorageWrapped[*api.E2EWidget]
	notes   *cl.MemoryStorageWrapped[*api.E2ERemoteNote]
}

func newWidgetEnv(t *testing.T, withNotes bool) *widgetEnv {
	t.Helper()
	set := cl.NewStorageSet()
	env := &widgetEnv{
		r:       NewWidgetReconciler[*api.E2EWidget](logger.New(0)),
		widgets: cl.NewMemoryStorageWrapped[*api.E2EWidget](),
	}
	cl.SetStorage[*api.E2EWidget](set, env.widgets)
	if withNotes {
		env.notes = cl.NewMemoryStorageWrapped[*api.E2ERemoteNote]()
		cl.SetStorage[*api.E2ERemoteNote](set, env.notes)
	}
	env.r.SetStorage(set)
	return env
}

func (e *widgetEnv) add(t *testing.T, name string, spec api.E2EWidgetSpec) *api.E2EWidget {
	t.Helper()
	w := &api.E2EWidget{
		Resource: resource.Resource{Namespace: "default", Name: name, ShardID: "s1", Version: 3},
		Spec:     spec,
	}
	require.NoError(t, e.widgets.Create(context.Background(), w))
	return w
}

func (e *widgetEnv) reconcile(w *api.E2EWidget) (cl.Result, error) {
	return e.r.Reconcile(context.Background(), w)
}

func (e *widgetEnv) stored(t *testing.T, name string) *api.E2EWidget {
	t.Helper()
	w, ok, err := e.widgets.Get(context.Background(), resource.ObjectKey{Namespace: "default", Name: name})
	require.NoError(t, err)
	require.True(t, ok, "widget %s must exist", name)
	return w
}

// passFinalizer выполняет первый проход (добавление финализатора).
func (e *widgetEnv) passFinalizer(t *testing.T, w *api.E2EWidget) {
	t.Helper()
	res, err := e.reconcile(w)
	require.NoError(t, err)
	require.True(t, res.Requeue)
}

func TestWidget_NoStorageIsNoop(t *testing.T) {
	r := NewWidgetReconciler[*api.E2EWidget](logger.New(0))
	r.SetStorage(cl.NewStorageSet())
	res, err := r.Reconcile(context.Background(), &api.E2EWidget{})
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
}

func TestWidget_AddsFinalizerThenReady(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{Marker: "m1"})

	env.passFinalizer(t, w)
	st := env.stored(t, "w1")
	require.Contains(t, st.Finalizers, e2eFinalizer)
	require.Equal(t, 0, st.CurrentVersion, "requeue must not mark version as observed")
	require.Equal(t, 1, st.Status.Reconciles)

	res, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
	st = env.stored(t, "w1")
	require.Equal(t, 3, st.CurrentVersion)
	require.Equal(t, "m1", st.Status.ObservedMarker)
	require.True(t, conditions.IsTrue(st, conditions.Ready))
	require.True(t, conditions.IsTrue(st, api.E2EWidgetSyncedCond))
	require.Equal(t, 2, st.Status.Reconciles)
	require.Len(t, st.Status.PassTimes, 2)
}

func TestWidget_FailTimes(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{FailTimes: 2})
	env.passFinalizer(t, w)

	for i := 1; i <= 2; i++ {
		_, err := env.reconcile(w)
		require.ErrorContains(t, err, "injected failure")
		st := env.stored(t, "w1")
		require.Equal(t, i, st.Status.Failures)
		require.Equal(t, 0, st.CurrentVersion)
		require.False(t, conditions.IsTrue(st, api.E2EWidgetSyncedCond))
	}
	_, err := env.reconcile(w)
	require.NoError(t, err)
	require.True(t, conditions.IsTrue(env.stored(t, "w1"), conditions.Ready))
}

func TestWidget_PanicTimesPersistsStatusAndRepanics(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{PanicTimes: 1})
	env.passFinalizer(t, w)

	require.PanicsWithValue(t, "e2e: injected panic 1/1", func() { _, _ = env.reconcile(w) })
	st := env.stored(t, "w1")
	require.Equal(t, 1, st.Status.Panics)

	_, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, 3, env.stored(t, "w1").CurrentVersion)
}

// knownBug пропускает тест известного бага, пока не задан E2E_KNOWN_BUGS (как в e2e).
func knownBug(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("E2E_KNOWN_BUGS") == "" {
		t.Skipf("known bug: %s (set E2E_KNOWN_BUGS=1 to run)", reason)
	}
}

// После прохода с ошибкой ресурс не должен быть Ready.
//
// Известный баг (api/conditions + типовой defer оператора): после прохода с финализатором в
// conditions остаётся только Ready=False; на следующем проходе SyncReady не видит ни одного
// False-условия и ставит Ready=True, хотя reconcile вернул ошибку.
func TestWidget_NotReadyAfterFailure(t *testing.T) {
	knownBug(t, "SyncReady marks a resource Ready after a failed reconcile")
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{FailTimes: 1})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.Error(t, err)
	require.False(t, conditions.IsTrue(env.stored(t, "w1"), conditions.Ready), "failed reconcile must not be Ready")
}

// Проход, завершившийся паникой, не должен помечать версию обработанной.
//
// Известный баг типового defer оператора (исходная фикстура / operator-layout): при панике
// result и reterr нулевые, defer вызывает SetCurrentVersion и UpdateStatus — ресурс пропадает
// из pending, хотя reconcile не завершился.
func TestWidget_PanicDoesNotMarkObserved(t *testing.T) {
	knownBug(t, "reconcile defer marks a panicked pass as observed (current_version = version)")
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{PanicTimes: 1})
	env.passFinalizer(t, w)
	require.Panics(t, func() { _, _ = env.reconcile(w) })
	require.Equal(t, 0, env.stored(t, "w1").CurrentVersion, "panicked pass must not mark version as observed")
}

func TestWidget_RequeueExtra(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{RequeueExtra: 2})
	env.passFinalizer(t, w)
	for i := 1; i <= 2; i++ {
		res, err := env.reconcile(w)
		require.NoError(t, err)
		require.True(t, res.Requeue)
		require.Equal(t, i, w.Status.ReconcilePasses)
	}
	res, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
}

func TestWidget_RequeueAfter(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{RequeueAfterMs: 1500, RequeueAfterTimes: 1})
	env.passFinalizer(t, w)
	res, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, 1500*time.Millisecond, res.RequeueAfter)
	res, err = env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
	require.Equal(t, 1, w.Status.RequeueAfters)
}

func TestWidget_SetLabels(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{SetLabels: map[string]string{"managed": "true"}})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, "true", env.stored(t, "w1").Labels["managed"])
}

func TestWidget_LabelQueries(t *testing.T) {
	env := newWidgetEnv(t, false)
	env.add(t, "other", api.E2EWidgetSpec{})
	w := env.add(t, "w1", api.E2EWidgetSpec{LabelTestQueries: []string{"team in (a)"}})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.NoError(t, err)
	// MemoryStorageWrapped.List не фильтрует: важно, что результат записан в статус.
	require.Len(t, w.Status.LabelQueryResults, 1)
	require.Equal(t, "team in (a)", w.Status.LabelQueryResults[0].Selector)
	require.Equal(t, []string{"other", "w1"}, w.Status.LabelQueryResults[0].Names)
}

func TestWidget_LabelQueriesInvalidSelector(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{LabelTestQueries: []string{"team ==="}})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.ErrorContains(t, err, "labelTestQueries")
	require.False(t, conditions.IsTrue(env.stored(t, "w1"), api.E2EWidgetSyncedCond))
}

func TestWidget_ChildrenCreatedAndAwaited(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "parent", api.E2EWidgetSpec{Children: []string{"c1", "c2"}})
	env.passFinalizer(t, w)

	res, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, childWaitInterval, res.RequeueAfter, "parent waits for children")
	for _, name := range []string{"c1", "c2"} {
		c := env.stored(t, name)
		require.Equal(t, "s1", c.ShardID)
		require.Equal(t, "parent", c.Labels["e2e-parent"])
	}

	for _, name := range []string{"c1", "c2"} {
		c := env.stored(t, name)
		env.passFinalizer(t, c)
		_, err := env.reconcile(c)
		require.NoError(t, err)
	}
	res, err = env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
	require.ElementsMatch(t, []string{"c1", "c2"}, w.Status.ChildrenReady)
}

func TestWidget_RemoteNotes(t *testing.T) {
	env := newWidgetEnv(t, true)
	w := env.add(t, "w1", api.E2EWidgetSpec{RemoteNotes: []string{"n1"}})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.NoError(t, err)
	n, ok, err := env.notes.Get(context.Background(), resource.ObjectKey{Namespace: "default", Name: "n1"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "w1", n.Spec.Owner)
	require.Equal(t, []string{"n1"}, w.Status.RemoteNotes)
}

func TestWidget_RemoteNotesWithoutClient(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{RemoteNotes: []string{"n1"}})
	env.passFinalizer(t, w)
	_, err := env.reconcile(w)
	require.ErrorContains(t, err, "remote client")
}

func TestWidget_DeleteKeepsForeignFinalizers(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{})
	w.Finalizers = []string{"other/finalizer"}
	env.passFinalizer(t, w)
	require.ElementsMatch(t, []string{"other/finalizer", e2eFinalizer}, w.Finalizers)

	w.DeletionTimestamp = "2026-01-01 00:00:00"
	res, err := env.reconcile(w)
	require.NoError(t, err)
	require.Equal(t, cl.Result{}, res)
	require.Equal(t, []string{"other/finalizer"}, env.stored(t, "w1").Finalizers)
}

func TestWidget_PassTimesCapped(t *testing.T) {
	env := newWidgetEnv(t, false)
	w := env.add(t, "w1", api.E2EWidgetSpec{RequeueExtra: maxPassTimes + 10})
	for range maxPassTimes + 5 {
		_, err := env.reconcile(w)
		require.NoError(t, err)
	}
	require.Len(t, w.Status.PassTimes, maxPassTimes)
	require.Equal(t, maxPassTimes+5, w.Status.Reconciles)
}

func TestWidget_ParallelismTracking(t *testing.T) {
	r := NewWidgetReconciler[*api.E2EWidget](logger.New(0))
	k := resource.ObjectKey{Namespace: "default", Name: "a"}
	overlap, running := r.enter(k)
	require.False(t, overlap)
	require.Equal(t, 1, running)
	overlap, running = r.enter(k)
	require.True(t, overlap)
	require.Equal(t, 2, running)
	r.leave(k)
	r.leave(k)
	_, running = r.enter(resource.ObjectKey{Namespace: "default", Name: "b"})
	require.Equal(t, 1, running)
	require.NotContains(t, r.active, k)
}

func TestGadget_Ready(t *testing.T) {
	set := cl.NewStorageSet()
	store := cl.NewMemoryStorageWrapped[*api.E2EGadget]()
	cl.SetStorage[*api.E2EGadget](set, store)
	r := NewGadgetReconciler[*api.E2EGadget](logger.New(0))
	r.SetStorage(set)

	g := &api.E2EGadget{Resource: resource.Resource{Namespace: "default", Name: "g1", Version: 2}, Spec: api.E2EGadgetSpec{Marker: "x"}}
	require.NoError(t, store.Create(context.Background(), g))
	_, err := r.Reconcile(context.Background(), g)
	require.NoError(t, err)
	require.True(t, conditions.IsTrue(g, conditions.Ready))
	require.Equal(t, 2, g.CurrentVersion)
	require.Equal(t, "x", g.Status.ObservedMarker)

	g.DeletionTimestamp = "2026-01-01 00:00:00"
	_, err = r.Reconcile(context.Background(), g)
	require.NoError(t, err)
	require.Equal(t, 1, g.Status.Reconciles, "deleted gadget is not reconciled")
}
