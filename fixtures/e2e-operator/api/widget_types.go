package api

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
)

const (
	E2EWidgetKind  = "e2e-widget"
	E2EWidgetGroup = "e2e.reconcile-kit.dev"
)

// E2EWidget тестовый ресурс для e2e (аналог ExampleResource из operator-layout).
type E2EWidget struct {
	resource.Resource
	Spec   E2EWidgetSpec   `json:"spec"`
	Status E2EWidgetStatus `json:"status"`
}

type E2EWidgetSpec struct {
	// RequeueExtra — сколько дополнительных проходов reconcile с Requeue=true до Ready.
	RequeueExtra int `json:"requeueExtra,omitempty"`
	// LabelTestQueries — строки селекторов в формате state-manager / api (например `team in (a)` или
	// `team in (a),env in (shared)`). В reconcile для каждого вызывается Storage.List с LabelSelectors.
	LabelTestQueries []string `json:"labelTestQueries,omitempty"`
}

// LabelQueryResult результат List по одному селектору (для проверок в e2e).
type LabelQueryResult struct {
	Selector string   `json:"selector"`
	Count    int      `json:"count"`
	Names    []string `json:"names"`
}

type E2EWidgetStatus struct {
	Conditions         []conditions.Condition `json:"conditions"`
	ReconcilePasses    int                    `json:"reconcilePasses,omitempty"`
	ObservedGeneration int64                  `json:"observedGeneration,omitempty"`
	LabelQueryResults  []LabelQueryResult     `json:"labelQueryResults,omitempty"`
}

func (w *E2EWidget) GetConditions() []conditions.Condition {
	return w.Status.Conditions
}

func (w *E2EWidget) SetConditions(c []conditions.Condition) {
	w.Status.Conditions = c
}

func (w *E2EWidget) GetGK() resource.GroupKind {
	return resource.GroupKind{Kind: E2EWidgetKind, Group: E2EWidgetGroup}
}

func (w *E2EWidget) DeepCopy() *E2EWidget {
	return resource.DeepCopyStruct(w).(*E2EWidget)
}
