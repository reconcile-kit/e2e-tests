package controllers

import (
	"context"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/pkg/logger"
)

// GadgetReconciler — минимальный второй контроллер: без финализатора, сразу Ready.
type GadgetReconciler[T resource.Object[T]] struct {
	storage *cl.StorageSet
	log     *logger.Logger
}

func NewGadgetReconciler[T resource.Object[T]](l *logger.Logger) *GadgetReconciler[T] {
	return &GadgetReconciler[T]{log: l}
}

func (r *GadgetReconciler[T]) SetStorage(storage *cl.StorageSet) {
	r.storage = storage
}

func (r *GadgetReconciler[T]) Reconcile(ctx context.Context, object *api.E2EGadget) (cl.Result, error) {
	client, ok := cl.GetStorage[*api.E2EGadget](r.storage)
	if !ok {
		return cl.Result{}, nil
	}
	if object.DeletionTimestamp != "" {
		return cl.Result{}, nil
	}
	object.Status.Reconciles++
	object.Status.ObservedMarker = object.Spec.Marker
	conditions.MarkTrue(object, api.E2EGadgetSyncedCond)
	conditions.SyncReady(object)
	object.SetCurrentVersion(object.GetVersion())
	if err := client.UpdateStatus(ctx, object); err != nil {
		r.log.Errorf("gadget UpdateStatus: %v", err)
		return cl.Result{}, err
	}
	return cl.Result{}, nil
}
