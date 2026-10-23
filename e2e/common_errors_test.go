//go:build integration

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	unstructured "github.com/reconcile-kit/controlloop/common/unstructured_resource"
	"github.com/reconcile-kit/controlloop/common/watchedby/selectors"
	"github.com/reconcile-kit/controlloop/common/watchedby/structured"
	unstructuredwatch "github.com/reconcile-kit/controlloop/common/watchedby/unstructured"
	provider "github.com/reconcile-kit/state-manager-provider"
	"github.com/stretchr/testify/require"
)

// notificationConsumer is the known schema used by the typed client in this
// boundary regression. The controller fixture is exercised by the operator tests.
type notificationConsumer struct {
	resource.Resource
	Spec   json.RawMessage `json:"spec"`
	Status json.RawMessage `json:"status"`
}

func (c *notificationConsumer) GetGK() resource.GroupKind {
	return resource.GroupKind{Group: widgetGroup, Kind: consumerKind}
}
func (c *notificationConsumer) DeepCopy() *notificationConsumer {
	return resource.DeepCopyStruct(c).(*notificationConsumer)
}

// The selector's local predicate is a deterministic barrier: List has completed,
// then the real resource is deleted, then the notifier performs its real Update.
// This exposes the impact of the existing MissingResourceIs404 state-manager bug
// on both common notifiers without sleeps or a probabilistic deletion race.
func TestE2E_CommonWatchedByDeletedTarget(t *testing.T) {
	skipIfNoEnv(t)
	knownBug(t, "state-manager returns HTTP 500 instead of 404 when a watched resource disappears between List and Update; notifier cannot skip NotFound")
	for _, mode := range []string{"structured", "unstructured"} {
		t.Run(mode, func(t *testing.T) {
			shard, sm := setupShard(t)
			name := uniqueName("deleted-target")
			createConsumer(t, sm, shard, name, "unused", "unused", true, false)
			deletions := 0
			remove := func() {
				code, _, err := sm.Do(http.MethodDelete, sm.kindResourcePath(consumerKind, name), nil)
				require.NoError(t, err)
				require.Equal(t, http.StatusNoContent, code)
				deletions++
			}
			var err error
			if mode == "structured" {
				storage, createErr := provider.NewStateManagerProvider[*notificationConsumer](os.Getenv("E2E_STORAGE_URL"), &http.Client{Transport: commonTransport{token: sm.token}})
				require.NoError(t, createErr)
				remote, createErr := cl.NewRemoteClient[*notificationConsumer](storage)
				require.NoError(t, createErr)
				selector := selectors.NewNameSelector[*notificationConsumer](name, testNamespace)
				selector.WithFilters(func(*notificationConsumer) bool { remove(); return true })
				err = structured.NewNotifier(remote, shard).NotifyWatchedByResources(t.Context(), selector)
			} else {
				client := newCommonClient(t, sm, unstructured.MetadataProvider{Group: widgetGroup, Kind: consumerKind, ShardID: shard})
				selector := selectors.NewNameSelector[*unstructured.UnstructuredResource](name, testNamespace)
				selector.WithFilters(func(*unstructured.UnstructuredResource) bool { remove(); return true })
				err = unstructuredwatch.NewNotifier(client).NotifyWatchedByResources(t.Context(), selector)
			}
			require.Equal(t, 1, deletions)
			require.NoError(t, err, "a target deleted after List must be skipped")
		})
	}
}

func TestE2E_CommonWatchedByAuthFailureAndRestart(t *testing.T) {
	skipIfNoAuth(t)
	for _, mode := range []string{"structured", "unstructured"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			source, sm := setupShard(t)
			target := newShard()
			name, consumer := uniqueName("config-retry"), uniqueName("consumer-retry")
			created := createConsumer(t, sm, target, consumer, name, name, true, false)
			createConfig(t, sm, source, target, name, mode, "", consumer)
			denied := startOperator(t, operatorOpts{Shard: source}) // Own shard only.
			waitCommon(t, sm, configKind, name, func(obj *ResourceDTO, status commonStatus) bool {
				if obj.CurrentVersion >= obj.Version || status.ResolvedValue != "v1" {
					return false
				}
				for _, condition := range status.Conditions {
					if condition.Type == "DependentsNotified" {
						return condition.Status == "False" && condition.Reason == "NOTIFY_FAILED"
					}
				}
				return false
			})
			pending, code, err := sm.GetKind(consumerKind, consumer)
			require.NoError(t, err)
			require.Equal(t, 200, code)
			require.Equal(t, created.Version, pending.Version)
			// Simulate a crash after failed notification: the pending version must
			// survive and be picked up by the replacement operator's ListPending.
			require.NoError(t, denied.cmd.Process.Kill())
			_, exited := denied.WaitExit(operatorReadyWait)
			require.True(t, exited, "the failed operator must exit before its replacement starts")
			startOperator(t, operatorOpts{Shard: source, Token: commonToken(t, source, target, consumerKind, []string{"get", "list", "update"})})
			config := waitConfig(t, sm, name, "v1")
			pending, code, err = sm.GetKind(consumerKind, consumer)
			require.NoError(t, err)
			require.Equal(t, 200, code)
			require.Greater(t, pending.Version, created.Version)
			startOperator(t, operatorOpts{Shard: target, Token: commonToken(t, target, source, configKind, []string{"get"})})
			waitConsumer(t, sm, consumer, "v1", config.Version)
		})
	}
}
