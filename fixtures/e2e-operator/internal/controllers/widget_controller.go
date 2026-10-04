package controllers

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/pkg/logger"
)

const e2eFinalizer = "e2e.reconcile-kit.dev/finalizer"

// maxPassTimes ограничивает status.passTimes, чтобы статус не рос бесконечно.
const maxPassTimes = 50

// childWaitInterval — как часто перепроверять готовность дочерних виджетов.
const childWaitInterval = 500 * time.Millisecond

type WidgetReconciler[T resource.Object[T]] struct {
	storage *cl.StorageSet
	log     *logger.Logger

	// Учёт параллельных reconcile: сколько идёт всего и по каждому ключу.
	mu      sync.Mutex
	running int
	active  map[resource.ObjectKey]int
}

func NewWidgetReconciler[T resource.Object[T]](l *logger.Logger) *WidgetReconciler[T] {
	return &WidgetReconciler[T]{log: l, active: map[resource.ObjectKey]int{}}
}

func (r *WidgetReconciler[T]) SetStorage(storage *cl.StorageSet) {
	r.storage = storage
}

// enter регистрирует начало reconcile key и возвращает, шёл ли уже reconcile того же ключа,
// и сколько reconcile идёт сейчас всего (включая этот).
func (r *WidgetReconciler[T]) enter(key resource.ObjectKey) (overlap bool, running int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	overlap = r.active[key] > 0
	r.active[key]++
	r.running++
	return overlap, r.running
}

func (r *WidgetReconciler[T]) leave(key resource.ObjectKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[key]--
	if r.active[key] == 0 {
		delete(r.active, key)
	}
	r.running--
}

func (r *WidgetReconciler[T]) Reconcile(ctx context.Context, object *api.E2EWidget) (result cl.Result, reterr error) {
	client, ok := cl.GetStorage[*api.E2EWidget](r.storage)
	if !ok {
		return cl.Result{}, nil
	}

	key := object.GetName()
	overlap, running := r.enter(key)
	defer r.leave(key)
	r.observePass(object, overlap, running)

	// Шаблон defer как в исходной фикстуре (operator-layout): статус сохраняется после
	// каждого прохода, Ready считается через SyncReady. Намеренно без обходов — e2e должны
	// видеть, как ведёт себя типовой оператор (см. known-bug тесты про SyncReady и панику).
	defer func() {
		empty := cl.Result{}
		if reterr != nil {
			r.log.Error(reterr)
		}
		if result == empty && reterr == nil {
			object.SetCurrentVersion(object.GetVersion())
		}
		conditions.SyncReady(object)
		if err := client.UpdateStatus(ctx, object); err != nil {
			reterr = err
			r.log.Errorf("UpdateStatus: %v", err)
		}
	}()

	if object.Spec.SleepMs > 0 {
		time.Sleep(time.Duration(object.Spec.SleepMs) * time.Millisecond)
	}

	if object.DeletionTimestamp != "" {
		return r.reconcileDelete(ctx, object)
	}

	if !slices.Contains(object.Finalizers, e2eFinalizer) {
		object.Finalizers = append(object.Finalizers, e2eFinalizer)
		return cl.Result{Requeue: true}, nil
	}

	if object.Status.Panics < object.Spec.PanicTimes {
		object.Status.Panics++
		panic(fmt.Sprintf("e2e: injected panic %d/%d", object.Status.Panics, object.Spec.PanicTimes))
	}

	if object.Status.Failures < object.Spec.FailTimes {
		object.Status.Failures++
		return cl.Result{}, fmt.Errorf("e2e: injected failure %d/%d", object.Status.Failures, object.Spec.FailTimes)
	}

	extra := object.Spec.RequeueExtra
	if extra > 0 && object.Status.ReconcilePasses < extra {
		object.Status.ReconcilePasses++
		object.Status.ObservedGeneration = object.GetGeneration()
		return cl.Result{Requeue: true}, nil
	}

	if object.Spec.RequeueAfterMs > 0 && object.Status.RequeueAfters < object.Spec.RequeueAfterTimes {
		object.Status.RequeueAfters++
		return cl.Result{RequeueAfter: time.Duration(object.Spec.RequeueAfterMs) * time.Millisecond}, nil
	}

	if len(object.Spec.SetLabels) > 0 {
		if object.Labels == nil {
			object.Labels = map[string]string{}
		}
		for k, v := range object.Spec.SetLabels {
			object.Labels[k] = v
		}
	}

	if len(object.Spec.Children) > 0 {
		allReady, err := r.reconcileChildren(ctx, client, object)
		if err != nil {
			return cl.Result{}, err
		}
		if !allReady {
			return cl.Result{RequeueAfter: childWaitInterval}, nil
		}
	}

	if len(object.Spec.RemoteNotes) > 0 {
		if err := r.reconcileRemoteNotes(ctx, object); err != nil {
			return cl.Result{}, err
		}
	}

	if len(object.Spec.LabelTestQueries) > 0 {
		object.Status.LabelQueryResults = nil
		for _, selStr := range object.Spec.LabelTestQueries {
			sels, err := resource.ParseLabelSelectors(selStr)
			if err != nil {
				return cl.Result{}, fmt.Errorf("labelTestQueries %q: %w", selStr, err)
			}
			m, err := client.List(ctx, resource.ListOpts{
				Namespace:      object.Namespace,
				ShardID:        object.ShardID,
				LabelSelectors: sels,
			})
			if err != nil {
				return cl.Result{}, fmt.Errorf("List %q: %w", selStr, err)
			}
			names := make([]string, 0, len(m))
			for k := range m {
				names = append(names, k.Name)
			}
			sort.Strings(names)
			object.Status.LabelQueryResults = append(object.Status.LabelQueryResults, api.LabelQueryResult{
				Selector: selStr,
				Count:    len(m),
				Names:    names,
			})
		}
	}

	object.Status.ObservedMarker = object.Spec.Marker
	conditions.MarkTrue(object, api.E2EWidgetSyncedCond)
	object.Status.ObservedGeneration = object.GetGeneration()
	return cl.Result{}, nil
}

// observePass записывает в статус метаданные прохода: счётчик, время, параллелизм.
func (r *WidgetReconciler[T]) observePass(object *api.E2EWidget, overlap bool, running int) {
	object.Status.Reconciles++
	object.Status.PassTimes = append(object.Status.PassTimes, time.Now().UTC().Format(time.RFC3339Nano))
	if n := len(object.Status.PassTimes); n > maxPassTimes {
		object.Status.PassTimes = object.Status.PassTimes[n-maxPassTimes:]
	}
	if running > object.Status.MaxParallel {
		object.Status.MaxParallel = running
	}
	if overlap {
		object.Status.SameKeyOverlap = true
	}
}

// reconcileChildren создаёт недостающих детей через Storage.Create (тот же шард и namespace)
// и сообщает, все ли они уже Ready.
func (r *WidgetReconciler[T]) reconcileChildren(ctx context.Context, client cl.Storage[*api.E2EWidget], object *api.E2EWidget) (bool, error) {
	ready := make([]string, 0, len(object.Spec.Children))
	for _, name := range object.Spec.Children {
		child, exist, err := client.Get(ctx, resource.ObjectKey{Namespace: object.Namespace, Name: name})
		if err != nil {
			return false, fmt.Errorf("get child %s: %w", name, err)
		}
		if !exist {
			child = &api.E2EWidget{Resource: resource.Resource{
				Namespace: object.Namespace,
				Name:      name,
				ShardID:   object.ShardID,
				Labels:    map[string]string{"e2e-parent": object.Name},
			}}
			if err := client.Create(ctx, child); err != nil {
				return false, fmt.Errorf("create child %s: %w", name, err)
			}
			continue
		}
		if conditions.IsTrue(child, conditions.Ready) {
			ready = append(ready, name)
		}
	}
	object.Status.ChildrenReady = ready
	return len(ready) == len(object.Spec.Children), nil
}

// reconcileRemoteNotes создаёт E2ERemoteNote через RemoteClient (тип без своего контроллера).
func (r *WidgetReconciler[T]) reconcileRemoteNotes(ctx context.Context, object *api.E2EWidget) error {
	notes, ok := cl.GetStorage[*api.E2ERemoteNote](r.storage)
	if !ok {
		return fmt.Errorf("remote client for %s is not registered", api.E2ERemoteNoteKind)
	}
	created := make([]string, 0, len(object.Spec.RemoteNotes))
	for _, name := range object.Spec.RemoteNotes {
		key := resource.ObjectKey{Namespace: object.Namespace, Name: name}
		_, exist, err := notes.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("get remote note %s: %w", name, err)
		}
		if !exist {
			note := &api.E2ERemoteNote{
				Resource: resource.Resource{
					ResourceGroup: api.E2EWidgetGroup,
					Kind:          api.E2ERemoteNoteKind,
					Namespace:     object.Namespace,
					Name:          name,
					ShardID:       object.ShardID,
				},
				Spec: api.E2ERemoteNoteSpec{Owner: object.Name},
			}
			if err := notes.Create(ctx, note); err != nil {
				return fmt.Errorf("create remote note %s: %w", name, err)
			}
		}
		created = append(created, name)
	}
	object.Status.RemoteNotes = created
	return nil
}

// reconcileDelete снимает только свой финализатор: чужие остаются, и state-manager
// удалит ресурс, когда их снимут владельцы.
func (r *WidgetReconciler[T]) reconcileDelete(_ context.Context, object *api.E2EWidget) (cl.Result, error) {
	r.log.Infof("reconcile delete %s/%s", object.Namespace, object.Name)
	object.Finalizers = slices.DeleteFunc(slices.Clone(object.Finalizers), func(f string) bool {
		return f == e2eFinalizer
	})
	return cl.Result{}, nil
}
