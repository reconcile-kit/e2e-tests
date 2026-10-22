package config

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reconcile-kit/api/conditions"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
)

type Notifier interface {
	Notify(context.Context, *api.E2EConfig) error
}

type Reconciler struct {
	storage  *cl.StorageSet
	notifier Notifier
}

func NewReconciler(notifier Notifier) *Reconciler       { return &Reconciler{notifier: notifier} }
func (r *Reconciler) SetStorage(storage *cl.StorageSet) { r.storage = storage }

func (r *Reconciler) Reconcile(ctx context.Context, obj *api.E2EConfig) (cl.Result, error) {
	client, ok := cl.GetStorage[*api.E2EConfig](r.storage)
	if !ok {
		return cl.Result{}, fmt.Errorf("config storage missing")
	}
	if obj.DeletionTimestamp != "" {
		return r.reconcileDelete(ctx, client, obj)
	}
	if !slices.Contains(obj.Finalizers, api.E2EConfigFinalizer) {
		obj.AddFinalizer(api.E2EConfigFinalizer)
		return cl.Result{Requeue: true}, client.UpdateStatus(ctx, obj)
	}
	if obj.GetCurrentVersion() == obj.GetVersion() {
		return cl.Result{}, nil
	}
	return r.reconcileNormal(ctx, client, obj)
}

func (r *Reconciler) reconcileNormal(ctx context.Context, client cl.Storage[*api.E2EConfig], obj *api.E2EConfig) (cl.Result, error) {
	setDefaultConditionsForConfig(obj)
	obj.Status.ResolvedValue = obj.Spec.Value
	obj.Status.ResolvedVersion = obj.Version
	conditions.MarkTrue(obj, api.ConfigResolvedCond)
	conditions.SyncReady(obj)
	// Publish the dependency's result before any dependent is awakened. Keep the
	// version pending until notification succeeds, so restart/retry can finish it.
	if err := client.UpdateStatus(ctx, obj); err != nil {
		return cl.Result{}, err
	}
	if err := r.notifier.Notify(ctx, obj); err != nil {
		conditions.MarkFalse(obj, api.DependentsNotifiedCond, api.ReasonNotifyFailed, err.Error())
		conditions.SyncReady(obj)
		return cl.Result{}, errors.Join(err, client.UpdateStatus(ctx, obj))
	}
	conditions.MarkTrue(obj, api.DependentsNotifiedCond)
	conditions.SyncReady(obj)
	obj.SetCurrentVersion(obj.Version)
	return cl.Result{}, client.UpdateStatus(ctx, obj)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, client cl.Storage[*api.E2EConfig], obj *api.E2EConfig) (cl.Result, error) {
	obj.RemoveFinalizer(api.E2EConfigFinalizer)
	return cl.Result{}, client.UpdateStatus(ctx, obj)
}
