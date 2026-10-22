package common_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reconcile-kit/api/resource"
	unstructured "github.com/reconcile-kit/controlloop/common/unstructured_resource"
	"github.com/stretchr/testify/require"
)

type rawStorage struct {
	resource.ExternalStorage[*unstructured.UnstructuredResource]
	object    *unstructured.UnstructuredResource
	failure   error
	found     bool
	gk        resource.GroupKind
	key       resource.ObjectKey
	opts      resource.ListOpts
	operation string
}

func (s *rawStorage) Create(_ context.Context, obj *unstructured.UnstructuredResource) error {
	s.operation = "create"
	s.object = obj
	return s.failure
}
func (s *rawStorage) Update(_ context.Context, obj *unstructured.UnstructuredResource) error {
	s.operation = "update"
	s.object = obj
	return s.failure
}
func (s *rawStorage) UpdateStatus(_ context.Context, obj *unstructured.UnstructuredResource) error {
	s.operation = "status"
	s.object = obj
	return s.failure
}
func (s *rawStorage) Get(_ context.Context, gk resource.GroupKind, key resource.ObjectKey) (*unstructured.UnstructuredResource, bool, error) {
	s.gk, s.key = gk, key
	return s.object, s.found, s.failure
}
func (s *rawStorage) List(_ context.Context, gk resource.GroupKind, opts resource.ListOpts) ([]*unstructured.UnstructuredResource, error) {
	s.gk, s.opts = gk, opts
	if s.failure != nil {
		return nil, s.failure
	}
	if s.object == nil {
		return nil, nil
	}
	return []*unstructured.UnstructuredResource{s.object}, nil
}
func (s *rawStorage) Delete(_ context.Context, gk resource.GroupKind, key resource.ObjectKey) error {
	s.gk, s.key, s.operation = gk, key, "delete"
	return s.failure
}

func TestUnstructuredClient_MetadataAndRouting(t *testing.T) {
	md := unstructured.MetadataProvider{Group: "external.example", Kind: "consumer", ShardID: "target"}
	storage := &rawStorage{}
	client := rawClient(t, storage, md)
	require.Equal(t, md, client.GetMetadataProvider())
	for _, tc := range []struct {
		name  string
		write func(context.Context, *unstructured.UnstructuredResource) error
	}{
		{"create", client.Create}, {"update", client.Update}, {"status", client.UpdateStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.UnstructuredResource{Resource: resource.Resource{Name: "a", Namespace: "ns", Kind: "wrong", ResourceGroup: "wrong", ShardID: "wrong"},
				Spec: json.RawMessage(`{"nested":{"list":[1,null,true]},"unknown":"keep"}`), Status: json.RawMessage(`{"answer":42}`)}
			require.NoError(t, tc.write(t.Context(), obj))
			require.Same(t, obj, storage.object)
			require.Equal(t, tc.name, storage.operation)
			require.Equal(t, md, *obj.MetadataProvider)
			require.Equal(t, md.Group, obj.ResourceGroup)
			require.Equal(t, md.Kind, obj.Kind)
			require.Equal(t, md.ShardID, obj.ShardID)
			encoded, err := json.Marshal(obj)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "MetadataProvider")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &fields))
			require.JSONEq(t, string(obj.Spec), string(fields["spec"]))
			require.JSONEq(t, string(obj.Status), string(fields["status"]))
		})
	}
	key := resource.ObjectKey{Namespace: "ns", Name: "a"}
	storage.object = &unstructured.UnstructuredResource{Resource: resource.Resource{Name: key.Name, Namespace: key.Namespace}}
	storage.found = true
	got, exists, err := client.Get(t.Context(), key)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, md, *got.MetadataProvider)
	require.Equal(t, resource.GroupKind{Group: md.Group, Kind: md.Kind}, storage.gk)
	require.Equal(t, key, storage.key)
	opts := resource.ListOpts{Namespace: "ns", ShardID: "target", Limit: 7, Offset: 2}
	got.MetadataProvider = nil
	items, err := client.List(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, opts, storage.opts)
	require.Equal(t, md, *items[key].MetadataProvider)
	require.NoError(t, client.Delete(t.Context(), key))
	require.Equal(t, "delete", storage.operation)
	require.Equal(t, key, storage.key)
	storage.object = nil
	items, err = client.List(t.Context(), resource.ListOpts{})
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestUnstructuredClient_ErrorsAndMissing(t *testing.T) {
	failure := errors.New("external unavailable")
	storage := &rawStorage{failure: failure, found: true}
	client := rawClient(t, storage, unstructured.MetadataProvider{Group: "g", Kind: "k", ShardID: "s"})
	obj := &unstructured.UnstructuredResource{}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"create", func() error { return client.Create(t.Context(), obj) }},
		{"update", func() error { return client.Update(t.Context(), obj) }},
		{"status", func() error { return client.UpdateStatus(t.Context(), obj) }},
		{"delete", func() error { return client.Delete(t.Context(), resource.ObjectKey{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) { require.ErrorIs(t, tc.call(), failure) })
	}
	_, found, err := client.Get(t.Context(), resource.ObjectKey{})
	require.ErrorIs(t, err, failure)
	require.False(t, found)
	items, err := client.List(t.Context(), resource.ListOpts{})
	require.ErrorIs(t, err, failure)
	require.Nil(t, items)
	storage.failure, storage.found = nil, false
	got, found, err := client.Get(t.Context(), resource.ObjectKey{})
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got)
}

func TestUnstructuredClient_IndependentKinds(t *testing.T) {
	a := unstructured.MetadataProvider{Group: "group-a", Kind: "kind-a", ShardID: "shard-a"}
	b := unstructured.MetadataProvider{Group: "group-b", Kind: "kind-b", ShardID: "shard-b"}
	storageA, storageB := &rawStorage{found: true}, &rawStorage{found: true}
	clientA, clientB := rawClient(t, storageA, a), rawClient(t, storageB, b)
	previous := unstructured.GetGlobalMetadataProvider()
	defer unstructured.SetGlobalMetadataProvider(previous)
	unstructured.SetGlobalMetadataProvider(unstructured.MetadataProvider{Group: "unrelated", Kind: "unrelated"})
	for _, tc := range []struct {
		name    string
		client  unstructured.UnstructuredClient
		storage *rawStorage
		md      unstructured.MetadataProvider
	}{
		{"a", clientA, storageA, a}, {"b", clientB, storageB, b},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.UnstructuredResource{Resource: resource.Resource{Name: "same", Namespace: "ns"}}
			require.NoError(t, tc.client.Create(t.Context(), obj))
			got, exists, err := tc.client.Get(t.Context(), obj.GetName())
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, resource.GroupKind{Group: tc.md.Group, Kind: tc.md.Kind}, tc.storage.gk)
			require.Equal(t, tc.storage.gk, got.GetGK())
			require.Equal(t, tc.md.ShardID, got.ShardID)
		})
	}
}

func TestUnstructuredResource_DeepCopy(t *testing.T) {
	t.Parallel()
	md := unstructured.MetadataProvider{Group: "g", Kind: "k", ShardID: "s"}
	obj := &unstructured.UnstructuredResource{Resource: resource.Resource{Labels: map[string]string{"k": "v"}, Annotations: map[string]string{"a": "v"}, Finalizers: []string{"keep"}},
		Spec: json.RawMessage(`{"value":1}`), Status: json.RawMessage(`{"value":2}`)}
	obj.SetMetadataProvider(&md)
	first, second := obj.DeepCopy(), obj.DeepCopy()
	first.Spec[9] = '3'
	first.Status[9] = '4'
	first.Labels["k"] = "changed"
	first.Annotations["a"] = "changed"
	first.Finalizers[0] = "changed"
	first.MetadataProvider.Kind = "changed"
	require.JSONEq(t, `{"value":1}`, string(obj.Spec))
	require.JSONEq(t, `{"value":2}`, string(obj.Status))
	require.Equal(t, obj, second)
	require.NotSame(t, obj.MetadataProvider, first.MetadataProvider)
	require.Equal(t, "k", obj.GetGK().Kind)
}

func TestUnstructuredResource_GlobalFallback(t *testing.T) {
	previous := unstructured.GetGlobalMetadataProvider()
	defer unstructured.SetGlobalMetadataProvider(previous)
	md := unstructured.MetadataProvider{Group: "global-g", Kind: "global-k"}
	unstructured.SetGlobalMetadataProvider(md)
	obj := &unstructured.UnstructuredResource{}
	require.Equal(t, resource.GroupKind{Group: md.Group, Kind: md.Kind}, obj.GetGK())
	obj.SetMetadataProvider(&unstructured.MetadataProvider{Group: "own-g", Kind: "own-k", ShardID: "own-s"})
	require.Equal(t, "own-k", obj.GetGK().Kind)
	obj.SetMetadataProvider(nil)
	require.Equal(t, resource.GroupKind{Group: md.Group, Kind: md.Kind}, obj.GetGK())
}
