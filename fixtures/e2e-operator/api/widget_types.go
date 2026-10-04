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

// E2EWidgetSpec — «ручки», которыми e2e-тесты управляют поведением reconcile.
type E2EWidgetSpec struct {
	// RequeueExtra — сколько дополнительных проходов reconcile с Requeue=true до Ready.
	RequeueExtra int `json:"requeueExtra,omitempty"`
	// LabelTestQueries — строки селекторов в формате state-manager / api (например `team in (a)` или
	// `team in (a),env in (shared)`). В reconcile для каждого вызывается Storage.List с LabelSelectors.
	LabelTestQueries []string `json:"labelTestQueries,omitempty"`

	// Marker копируется в status.observedMarker при успешном проходе: по нему тесты видят,
	// какую версию spec обработал оператор.
	Marker string `json:"marker,omitempty"`
	// FailTimes — сколько раз подряд вернуть ошибку (проверка повторов с backoff).
	FailTimes int `json:"failTimes,omitempty"`
	// PanicTimes — сколько раз подряд запаниковать (проверка recover в controlloop).
	PanicTimes int `json:"panicTimes,omitempty"`
	// RequeueAfterMs / RequeueAfterTimes — сколько проходов вернуть Result{RequeueAfter} и с какой задержкой.
	RequeueAfterMs    int `json:"requeueAfterMs,omitempty"`
	RequeueAfterTimes int `json:"requeueAfterTimes,omitempty"`
	// SleepMs — пауза в каждом проходе (расширяет окно гонок).
	SleepMs int `json:"sleepMs,omitempty"`
	// Children — имена дочерних виджетов, которые reconcile создаёт через Storage.Create.
	Children []string `json:"children,omitempty"`
	// SetLabels — лейблы, которые reconcile выставляет ресурсу (уходят через UpdateStatus).
	SetLabels map[string]string `json:"setLabels,omitempty"`
	// RemoteNotes — имена E2ERemoteNote, которые reconcile создаёт через RemoteClient.
	RemoteNotes []string `json:"remoteNotes,omitempty"`
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

	// Reconciles — число вызовов Reconcile (включая ошибки и delete).
	Reconciles     int    `json:"reconciles,omitempty"`
	Failures       int    `json:"failures,omitempty"`
	Panics         int    `json:"panics,omitempty"`
	RequeueAfters  int    `json:"requeueAfters,omitempty"`
	ObservedMarker string `json:"observedMarker,omitempty"`
	// PassTimes — время начала каждого прохода (RFC3339Nano, не больше 50 последних).
	PassTimes []string `json:"passTimes,omitempty"`
	// MaxParallel — максимум одновременно идущих reconcile (по всем ключам), замеченный этим ресурсом.
	MaxParallel int `json:"maxParallel,omitempty"`
	// SameKeyOverlap — reconcile этого ключа шёл параллельно с другим reconcile того же ключа.
	SameKeyOverlap bool     `json:"sameKeyOverlap,omitempty"`
	ChildrenReady  []string `json:"childrenReady,omitempty"`
	RemoteNotes    []string `json:"remoteNotes,omitempty"`
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
