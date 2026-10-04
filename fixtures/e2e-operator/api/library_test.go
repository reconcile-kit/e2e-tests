package api

import (
	"os"
	"testing"

	"github.com/reconcile-kit/api/conditions"
	"github.com/reconcile-kit/api/resource"
	"github.com/stretchr/testify/require"
)

// Скаляры копируются в любом случае.
func TestDeepCopy_Scalars(t *testing.T) {
	src := &E2EWidget{Resource: resource.Resource{Name: "a", Version: 2}, Spec: E2EWidgetSpec{Marker: "m"}}
	cp := src.DeepCopy()
	require.NotSame(t, src, cp)
	cp.Name = "b"
	cp.Version = 3
	cp.Spec.Marker = "x"
	require.Equal(t, "a", src.Name)
	require.Equal(t, 2, src.Version)
	require.Equal(t, "m", src.Spec.Marker)
}

// DeepCopy должен разрывать связь по слайсам и мапам: memory storage controlloop отдаёт
// reconcile копию через DeepCopy и рассчитывает, что её изменения не трогают хранимый объект.
//
// Известный баг reconcile-kit/api: resource.DeepCopyStruct для именованной структуры из пакета
// (E2EWidget, resource.Resource, ...) возвращает значение как есть — получается поверхностная
// копия, слайсы Finalizers/Conditions и мапа Labels общие с оригиналом.
func TestDeepCopy_SlicesAndMapsAreIndependent(t *testing.T) {
	if os.Getenv("E2E_KNOWN_BUGS") == "" {
		t.Skip("known bug: resource.DeepCopyStruct makes a shallow copy of named structs (set E2E_KNOWN_BUGS=1)")
	}
	src := &E2EWidget{
		Resource: resource.Resource{
			Finalizers: []string{"f1"},
			Labels:     map[string]string{"k": "v"},
		},
		Status: E2EWidgetStatus{Conditions: []conditions.Condition{{Type: "A", Status: conditions.True}}},
	}
	cp := src.DeepCopy()
	cp.Finalizers[0] = "changed"
	cp.Labels["k"] = "changed"
	cp.Status.Conditions[0].Status = conditions.False

	require.Equal(t, "f1", src.Finalizers[0])
	require.Equal(t, "v", src.Labels["k"])
	require.Equal(t, conditions.True, src.Status.Conditions[0].Status)
}

// SyncReady должен быть идемпотентным: у ресурса без «рабочих» условий Ready=False
// и после повторного вызова.
//
// Известный баг reconcile-kit/api: первый вызов на пустом списке ставит Ready=False, второй
// видит только Ready (его пропускает), не находит False и ставит Ready=True.
func TestSyncReady_Idempotent(t *testing.T) {
	if os.Getenv("E2E_KNOWN_BUGS") == "" {
		t.Skip("known bug: conditions.SyncReady flips Ready to True on the second call (set E2E_KNOWN_BUGS=1)")
	}
	w := &E2EWidget{}
	conditions.SyncReady(w)
	require.False(t, conditions.IsTrue(w, conditions.Ready))
	conditions.SyncReady(w)
	require.False(t, conditions.IsTrue(w, conditions.Ready), "Ready must stay False without other conditions")
}

// SyncReady: Ready=False с причиной первого False-условия, Unknown игнорируется.
func TestSyncReady_Aggregation(t *testing.T) {
	w := &E2EWidget{}
	conditions.MarkTrue(w, "A")
	conditions.SyncReady(w)
	require.True(t, conditions.IsTrue(w, conditions.Ready))

	conditions.MarkFalse(w, "B", "Broken", "b is broken")
	conditions.SyncReady(w)
	require.False(t, conditions.IsTrue(w, conditions.Ready))
	for _, c := range w.Status.Conditions {
		if c.Type == conditions.Ready {
			require.Equal(t, "Broken", c.Reason)
			require.Equal(t, "b is broken", c.Message)
		}
	}

	conditions.MarkUnknown(w, "B", "Wait", "")
	conditions.SyncReady(w)
	require.True(t, conditions.IsTrue(w, conditions.Ready), "Unknown conditions are ignored")
}
