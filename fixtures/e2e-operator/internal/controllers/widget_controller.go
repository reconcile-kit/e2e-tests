package controllers

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/pkg/logger"
)

const e2eFinalizer = "e2e.reconcile-kit.dev/finalizer"

type WidgetReconciler[T resource.Object[T]] struct {
	storage *cl.StorageSet
	log     *logger.Logger
}

func NewWidgetReconciler[T resource.Object[T]](l *logger.Logger) *WidgetReconciler[T] {
	return &WidgetReconciler[T]{log: l}
}

func (r *WidgetReconciler[T]) SetStorage(storage *cl.StorageSet) {
	r.storage = storage
}

func (r *WidgetReconciler[T]) Reconcile(ctx context.Context, object *api.E2EWidget) (result cl.Result, reterr error) {
	client, ok := cl.GetStorage[*api.E2EWidget](r.storage)
	if !ok {
		return cl.Result{}, nil
	}

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

	if object.DeletionTimestamp != "" {
		return r.reconcileDelete(ctx, object)
	}

	if !slices.Contains(object.Finalizers, e2eFinalizer) {
		object.Finalizers = append(object.Finalizers, e2eFinalizer)
		return cl.Result{Requeue: true}, nil
	}

	extra := object.Spec.RequeueExtra
	if extra > 0 && object.Status.ReconcilePasses < extra {
		object.Status.ReconcilePasses++
		object.Status.ObservedGeneration = object.GetGeneration()
		return cl.Result{Requeue: true}, nil
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

	conditions.MarkTrue(object, api.E2EWidgetSyncedCond)
	object.Status.ObservedGeneration = object.GetGeneration()
	return cl.Result{}, nil
}

func (r *WidgetReconciler[T]) reconcileDelete(_ context.Context, object *api.E2EWidget) (cl.Result, error) {
	r.log.Infof("reconcile delete %s/%s", object.Namespace, object.Name)
	object.Finalizers = nil
	return cl.Result{}, nil
}
