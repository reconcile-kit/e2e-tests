package consumer

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
)

func setDefaultConditionsForConsumer(obj *api.E2EConsumer) {
	conditions.MarkUnknown(obj, api.ConsumerSyncedCond, api.ReasonDefault, "")
}
