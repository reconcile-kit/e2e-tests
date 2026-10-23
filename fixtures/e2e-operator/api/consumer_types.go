package api

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
)

const (
	E2EConsumerKind      = "e2e-consumer"
	E2EConsumerFinalizer = "e2e.reconcile-kit.dev/consumer"
	ConsumerSyncedCond   = "ConsumerSynced"
)

type E2EConsumer struct {
	resource.Resource
	Spec   E2EConsumerSpec   `json:"spec"`
	Status E2EConsumerStatus `json:"status"`
}

type E2EConsumerSpec struct {
	ConfigName string `json:"configName"`
	Enabled    bool   `json:"enabled"`
	Paused     bool   `json:"paused"`
}

type E2EConsumerStatus struct {
	Conditions            []conditions.Condition `json:"conditions"`
	ObservedValue         string                 `json:"observedValue"`
	ObservedConfigVersion int                    `json:"observedConfigVersion"`
}

func (c *E2EConsumer) GetGK() resource.GroupKind {
	return resource.GroupKind{Group: E2EWidgetGroup, Kind: E2EConsumerKind}
}
func (c *E2EConsumer) DeepCopy() *E2EConsumer                 { return resource.DeepCopyStruct(c).(*E2EConsumer) }
func (c *E2EConsumer) GetConditions() []conditions.Condition  { return c.Status.Conditions }
func (c *E2EConsumer) SetConditions(v []conditions.Condition) { c.Status.Conditions = v }
