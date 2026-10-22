package api

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
)

const (
	E2EConfigKind          = "e2e-config"
	E2EConfigFinalizer     = "e2e.reconcile-kit.dev/config"
	ConfigResolvedCond     = "ConfigResolved"
	DependentsNotifiedCond = "DependentsNotified"
	NotifyStructured       = "structured"
	NotifyUnstructured     = "unstructured"
)

// E2EConfig publishes a value before waking resources that consume it.
type E2EConfig struct {
	resource.Resource
	Spec   E2EConfigSpec   `json:"spec"`
	Status E2EConfigStatus `json:"status"`
}

type E2EConfigSpec struct {
	Value  string           `json:"value"`
	Notify NotificationSpec `json:"notify"`
}

type NotificationSpec struct {
	Mode          string `json:"mode"`
	TargetShardID string `json:"targetShardID"`
	Name          string `json:"name,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
}

type E2EConfigStatus struct {
	Conditions      []conditions.Condition `json:"conditions"`
	ResolvedValue   string                 `json:"resolvedValue"`
	ResolvedVersion int                    `json:"resolvedVersion"`
}

func (c *E2EConfig) GetGK() resource.GroupKind {
	return resource.GroupKind{Group: E2EWidgetGroup, Kind: E2EConfigKind}
}
func (c *E2EConfig) DeepCopy() *E2EConfig                   { return resource.DeepCopyStruct(c).(*E2EConfig) }
func (c *E2EConfig) GetConditions() []conditions.Condition  { return c.Status.Conditions }
func (c *E2EConfig) SetConditions(v []conditions.Condition) { c.Status.Conditions = v }
