package config

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
)

func setDefaultConditionsForConfig(obj *api.E2EConfig) {
	conditions.MarkUnknown(obj, api.ConfigResolvedCond, api.ReasonDefault, "")
	conditions.MarkFalse(obj, api.DependentsNotifiedCond, api.ReasonDefault, "notification pending")
}
