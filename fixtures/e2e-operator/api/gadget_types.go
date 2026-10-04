package api

import (
	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
)

const (
	E2EGadgetKind     = "e2e-gadget"
	E2ERemoteNoteKind = "e2e-remote-note"
)

// E2EGadget — второй тип со своим контроллером в том же менеджере: проверка маршрутизации
// событий по GroupKind и нескольких control loop в одном процессе.
type E2EGadget struct {
	resource.Resource
	Spec   E2EGadgetSpec   `json:"spec"`
	Status E2EGadgetStatus `json:"status"`
}

type E2EGadgetSpec struct {
	Marker string `json:"marker,omitempty"`
}

type E2EGadgetStatus struct {
	Conditions     []conditions.Condition `json:"conditions"`
	Reconciles     int                    `json:"reconciles,omitempty"`
	ObservedMarker string                 `json:"observedMarker,omitempty"`
}

func (g *E2EGadget) GetConditions() []conditions.Condition  { return g.Status.Conditions }
func (g *E2EGadget) SetConditions(c []conditions.Condition) { g.Status.Conditions = c }

func (g *E2EGadget) GetGK() resource.GroupKind {
	return resource.GroupKind{Kind: E2EGadgetKind, Group: E2EWidgetGroup}
}

func (g *E2EGadget) DeepCopy() *E2EGadget {
	return resource.DeepCopyStruct(g).(*E2EGadget)
}

// E2ERemoteNote — тип без контроллера, доступный только через RemoteClient (SetRemoteClient).
type E2ERemoteNote struct {
	resource.Resource
	Spec E2ERemoteNoteSpec `json:"spec"`
}

type E2ERemoteNoteSpec struct {
	Owner string `json:"owner,omitempty"`
}

func (n *E2ERemoteNote) GetGK() resource.GroupKind {
	return resource.GroupKind{Kind: E2ERemoteNoteKind, Group: E2EWidgetGroup}
}

func (n *E2ERemoteNote) DeepCopy() *E2ERemoteNote {
	return resource.DeepCopyStruct(n).(*E2ERemoteNote)
}
