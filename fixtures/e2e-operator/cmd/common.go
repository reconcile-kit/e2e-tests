package main

import (
	"net/http"

	cl "github.com/reconcile-kit/controlloop"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/internal/controller/config"
	"github.com/reconcile-kit/e2e-fixture-operator/internal/controller/consumer"
	"github.com/reconcile-kit/e2e-fixture-operator/internal/notifications"
	rtm "github.com/reconcile-kit/runtime-manager"
	provider "github.com/reconcile-kit/state-manager-provider"
)

func registerCommonControllers(mgr *rtm.Manager, address string, httpClient *http.Client) error {
	notifier, err := notifications.NewNotifier(address, httpClient)
	if err != nil {
		return err
	}
	storage, err := provider.NewStateManagerProvider[*api.E2EConfig](address, httpClient)
	if err != nil {
		return err
	}
	configs, err := cl.NewRemoteClient[*api.E2EConfig](storage)
	if err != nil {
		return err
	}
	if err := rtm.SetController[*api.E2EConfig](mgr, config.NewReconciler(notifier)); err != nil {
		return err
	}
	return rtm.SetController[*api.E2EConsumer](mgr, consumer.NewReconciler(configs))
}
