//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	unstructured "github.com/reconcile-kit/controlloop/common/unstructured_resource"
	provider "github.com/reconcile-kit/state-manager-provider"
	"github.com/stretchr/testify/require"
)

var commonClientMu sync.Mutex

type commonTransport struct{ token string }

func (c commonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if c.token != "" {
		copy.Header.Set("Authorization", "Bearer "+c.token)
	}
	return http.DefaultTransport.RoundTrip(copy)
}

func newCommonClient(t *testing.T, sm *SMClient, md unstructured.MetadataProvider) unstructured.UnstructuredClient {
	t.Helper()
	storage, err := provider.NewStateManagerProvider[*unstructured.UnstructuredResource](os.Getenv("E2E_STORAGE_URL"), &http.Client{
		Timeout: 30 * time.Second, Transport: commonTransport{token: sm.token},
	})
	require.NoError(t, err)
	// The common package requires callers to serialize set + construct together.
	commonClientMu.Lock()
	defer commonClientMu.Unlock()
	previous := unstructured.GetGlobalMetadataProvider()
	defer unstructured.SetGlobalMetadataProvider(previous)
	unstructured.SetGlobalMetadataProvider(md)
	remote, err := cl.NewRemoteClient[*unstructured.UnstructuredResource](storage)
	require.NoError(t, err)
	return unstructured.NewUnstructuredClient(remote, md)
}

func TestE2E_CommonUnstructuredClient(t *testing.T) {
	t.Parallel()
	shard, sm := setupShard(t)
	if authEnabled() {
		sm = sm.WithToken(signToken(t, tokenOpts{Permissions: shardPermissions(shard)}))
	}
	rdb := redisClient(t)
	name := uniqueName("raw")
	mdA := unstructured.MetadataProvider{Group: widgetGroup, Kind: consumerKind, ShardID: shard}
	mdB := unstructured.MetadataProvider{Group: "external.e2e.reconcile-kit.dev", Kind: configKind, ShardID: shard}
	clientA, clientB := newCommonClient(t, sm, mdA), newCommonClient(t, sm, mdB)
	for _, tc := range []struct {
		name   string
		client unstructured.UnstructuredClient
		md     unstructured.MetadataProvider
	}{
		{"consumer", clientA, mdA}, {"external_config", clientB, mdB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			client := tc.client
			obj := &unstructured.UnstructuredResource{Resource: resource.Resource{Name: name, Namespace: testNamespace,
				Labels: map[string]string{"team": "common"}, Annotations: map[string]string{"keep": "yes"}},
				Spec: json.RawMessage(`{"unknown":{"items":[1,true,null,{"nested":"keep"}]},"large":9007199254740993}`)}
			require.NoError(t, client.Create(ctx, obj))
			key := obj.GetName()
			got, found, err := client.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, tc.md, *got.MetadataProvider)
			require.Equal(t, resource.GroupKind{Group: tc.md.Group, Kind: tc.md.Kind}, got.GetGK())
			require.Contains(t, string(got.Spec), "9007199254740993")
			require.JSONEq(t, string(obj.Spec), string(got.Spec))
			labels, err := resource.ParseLabelSelectors("team in (common)")
			require.NoError(t, err)
			items, err := client.List(ctx, resource.ListOpts{Namespace: testNamespace, Name: name, ShardID: shard, LabelSelectors: labels, Limit: 1})
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Equal(t, tc.md, *items[key].MetadataProvider)
			items, err = client.List(ctx, resource.ListOpts{Namespace: testNamespace, ShardID: shard, Limit: 1, Offset: 1})
			require.NoError(t, err)
			require.Empty(t, items)
			stale := got.DeepCopy()
			got.Spec = json.RawMessage(`{"unknown":{"items":[2,false,null]},"large":9007199254740993}`)
			require.NoError(t, client.Update(ctx, got))
			require.Greater(t, got.Version, stale.Version)
			require.ErrorIs(t, client.Update(ctx, stale), resource.ConflictError)
			beforeEvents := len(streamEvents(t, rdb, shard))
			got.Status = json.RawMessage(`{"foreignStatus":{"values":["a",null]},"large":9007199254740993}`)
			got.CurrentVersion = got.Version
			require.NoError(t, client.UpdateStatus(ctx, got))
			require.Equal(t, beforeEvents, len(streamEvents(t, rdb, shard)), "UpdateStatus must not emit an update event")
			stored, found, err := client.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, found)
			require.JSONEq(t, string(got.Spec), string(stored.Spec))
			require.JSONEq(t, string(got.Status), string(stored.Status))
			require.Contains(t, string(stored.Status), "9007199254740993")
			require.Equal(t, obj.Labels, stored.Labels)
			require.Equal(t, obj.Annotations, stored.Annotations)
			require.NoError(t, client.Update(ctx, stored)) // Unchanged spec must still emit an event.
			events := streamEvents(t, rdb, shard)
			require.Len(t, events, beforeEvents+1)
			require.Equal(t, "update", events[len(events)-1].Type)
			require.NoError(t, client.Delete(ctx, key))
			missing, found, err := client.Get(ctx, key)
			require.NoError(t, err)
			require.False(t, found)
			require.Nil(t, missing)
		})
	}
}

func TestE2E_CommonUnstructuredCanceledRequest(t *testing.T) {
	t.Parallel()
	shard, sm := setupShard(t)
	client := newCommonClient(t, sm, unstructured.MetadataProvider{Group: widgetGroup, Kind: consumerKind, ShardID: shard})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := client.Get(ctx, resource.ObjectKey{Namespace: testNamespace, Name: "canceled"})
	require.ErrorIs(t, err, context.Canceled)
}
