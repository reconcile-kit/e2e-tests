package consumer

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
)

type Reconciler struct {
	storage *cl.StorageSet
	configs *cl.RemoteClient[*api.E2EConfig]
}

func NewReconciler(configs *cl.RemoteClient[*api.E2EConfig]) *Reconciler {
	return &Reconciler{configs: configs}
}
func (r *Reconciler) SetStorage(storage *cl.StorageSet) { r.storage = storage }

func (r *Reconciler) Reconcile(ctx context.Context, obj *api.E2EConsumer) (cl.Result, error) {
	client, ok := cl.GetStorage[*api.E2EConsumer](r.storage)
	if !ok {
		return cl.Result{}, fmt.Errorf("consumer storage missing")
	}
	if obj.DeletionTimestamp != "" {
		return r.reconcileDelete(ctx, client, obj)
	}
	if !slices.Contains(obj.Finalizers, api.E2EConsumerFinalizer) {
		obj.AddFinalizer(api.E2EConsumerFinalizer)
		return cl.Result{Requeue: true}, client.UpdateStatus(ctx, obj)
	}
	return r.reconcileNormal(ctx, client, obj)
}

func (r *Reconciler) reconcileNormal(ctx context.Context, client cl.Storage[*api.E2EConsumer], obj *api.E2EConsumer) (cl.Result, error) {
	setDefaultConditionsForConsumer(obj)
	config, found, err := r.configs.Get(ctx, resource.ObjectKey{Namespace: obj.Namespace, Name: obj.Spec.ConfigName})
	if err == nil && !found {
		err = fmt.Errorf("config %q not found", obj.Spec.ConfigName)
	}
	if err != nil {
		conditions.MarkFalse(obj, api.ConsumerSyncedCond, api.ReasonConfigReadFailed, err.Error())
		conditions.SyncReady(obj)
		return cl.Result{}, errors.Join(err, client.UpdateStatus(ctx, obj))
	}
	obj.Status.ObservedValue = config.Status.ResolvedValue
	obj.Status.ObservedConfigVersion = config.Status.ResolvedVersion
	conditions.MarkTrue(obj, api.ConsumerSyncedCond)
	conditions.SyncReady(obj)
	obj.SetCurrentVersion(obj.Version)
	return cl.Result{}, client.UpdateStatus(ctx, obj)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, client cl.Storage[*api.E2EConsumer], obj *api.E2EConsumer) (cl.Result, error) {
	obj.RemoveFinalizer(api.E2EConsumerFinalizer)
	return cl.Result{}, client.UpdateStatus(ctx, obj)
}
