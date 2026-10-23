package common_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	unstructured "github.com/reconcile-kit/controlloop/common/unstructured_resource"
	"github.com/reconcile-kit/controlloop/common/watchedby/selectors"
	"github.com/reconcile-kit/controlloop/common/watchedby/structured"
	unstructuredwatch "github.com/reconcile-kit/controlloop/common/watchedby/unstructured"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/stretchr/testify/require"
)

// Unexpected operations hit the nil embedded interface and fail the test.
// Tests supply only the boundary operations each public contract requires.
type storageStub[T resource.Object[T]] struct {
	resource.ExternalStorage[T]
	list   func(context.Context, resource.GroupKind, resource.ListOpts) ([]T, error)
	update func(context.Context, T) error
}

func (s *storageStub[T]) List(ctx context.Context, gk resource.GroupKind, opts resource.ListOpts) ([]T, error) {
	return s.list(ctx, gk, opts)
}
func (s *storageStub[T]) Update(ctx context.Context, obj T) error { return s.update(ctx, obj) }

func TestNotifiers_Contracts(t *testing.T) {
	t.Run("structured", func(t *testing.T) {
		testNotifier(t, func(i int) *api.E2EConsumer {
			return &api.E2EConsumer{Resource: resource.Resource{Name: fmt.Sprint(i), Namespace: "ns"}}
		},
			func(s *storageStub[*api.E2EConsumer]) func(context.Context, selectors.Selector[*api.E2EConsumer]) error {
				client, err := cl.NewRemoteClient[*api.E2EConsumer](s)
				require.NoError(t, err)
				return structured.NewNotifier(client, "target-shard").NotifyWatchedByResources
			})
	})
	t.Run("unstructured", func(t *testing.T) {
		testNotifier(t, func(i int) *unstructured.UnstructuredResource {
			return &unstructured.UnstructuredResource{Resource: resource.Resource{Name: fmt.Sprint(i), Namespace: "ns"}}
		},
			func(s *storageStub[*unstructured.UnstructuredResource]) func(context.Context, selectors.Selector[*unstructured.UnstructuredResource]) error {
				client := rawClient(t, s, unstructured.MetadataProvider{Group: api.E2EWidgetGroup, Kind: api.E2EConsumerKind, ShardID: "target-shard"})
				return unstructuredwatch.NewNotifier(client).NotifyWatchedByResources
			})
	})
}

func testNotifier[T resource.Object[T]](t *testing.T, object func(int) T, notifier func(*storageStub[T]) func(context.Context, selectors.Selector[T]) error) {
	t.Helper()
	for _, tc := range []struct {
		name                            string
		count                           int
		listError, missing, updateError bool
	}{
		{name: "empty"}, {name: "all_pages", count: 501}, {name: "exact_page", count: 500},
		{name: "list_error_before_updates", count: 501, listError: true},
		{name: "not_found_is_skipped", count: 4, missing: true},
		{name: "conflict_stops_without_rollback", count: 4, updateError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listFailure := errors.New("list failed")
			calls, updates, committed := 0, 0, 0
			lastName := ""
			labels := []resource.LabelSelector{{Key: "config", Operator: resource.LabelSelectorOperatorIn, Values: []string{"a"}}}
			storage := &storageStub[T]{}
			storage.list = func(ctx context.Context, gk resource.GroupKind, opts resource.ListOpts) ([]T, error) {
				calls++
				require.Zero(t, updates, "collect all pages before any update")
				require.Equal(t, resource.GroupKind{Group: api.E2EWidgetGroup, Kind: api.E2EConsumerKind}, gk)
				require.Equal(t, resource.ListOpts{Namespace: "ns", ShardID: "target-shard", LabelSelectors: labels, Limit: 500, Offset: (calls - 1) * 500}, opts)
				if tc.listError && calls == 2 {
					return nil, listFailure
				}
				items := []T{}
				for i := opts.Offset; i < min(tc.count, opts.Offset+opts.Limit); i++ {
					items = append(items, object(i))
				}
				return items, nil
			}
			storage.update = func(_ context.Context, obj T) error {
				updates++
				lastName = obj.GetName().Name
				if tc.missing && updates == 1 {
					return fmt.Errorf("deleted: %w", resource.NotFoundError)
				}
				if tc.updateError && updates == 2 {
					return fmt.Errorf("stale: %w", resource.ConflictError)
				}
				committed++
				return nil
			}
			err := notifier(storage)(t.Context(), selectors.NewLabelSelector[T](labels, "ns"))
			if tc.listError {
				require.ErrorIs(t, err, listFailure)
				require.ErrorContains(t, err, "offset 500")
				require.Zero(t, updates)
				return
			}
			if tc.updateError {
				require.ErrorIs(t, err, resource.ConflictError)
				require.ErrorContains(t, err, fmt.Sprintf("%q/%q", "ns", lastName))
				require.Equal(t, 2, updates)
				require.Equal(t, 1, committed)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.count/500+1, calls)
			require.Equal(t, tc.count, updates)
		})
	}
	t.Run("name_and_local_filters", func(t *testing.T) {
		var updated []string
		storage := &storageStub[T]{
			list: func(_ context.Context, _ resource.GroupKind, opts resource.ListOpts) ([]T, error) {
				require.Equal(t, "chosen", opts.Name)
				require.Equal(t, "ns", opts.Namespace)
				require.Equal(t, "target-shard", opts.ShardID)
				return []T{object(0), object(1), object(2)}, nil
			},
			update: func(_ context.Context, obj T) error { updated = append(updated, obj.GetName().Name); return nil },
		}
		selector := selectors.NewNameSelector[T]("chosen", "ns", selectors.WithFiltersSelectorOpts[T](func(obj T) bool { return obj.GetName().Name != "0" }))
		selector.WithFilters(func(obj T) bool { return obj.GetName().Name != "2" })
		require.NoError(t, notifier(storage)(t.Context(), selector))
		require.Equal(t, []string{"1"}, updated)
		selector.WithFilters(func(T) bool { return false })
		require.NoError(t, notifier(storage)(t.Context(), selector))
		require.Equal(t, []string{"1"}, updated, "all predicates must pass")
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := notifier(&storageStub[T]{})(ctx, selectors.NewNameSelector[T]("a", "ns"))
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("retry_may_notify_again", func(t *testing.T) {
		calls := 0
		storage := &storageStub[T]{list: func(context.Context, resource.GroupKind, resource.ListOpts) ([]T, error) { return []T{object(0)}, nil },
			update: func(context.Context, T) error { calls++; return nil }}
		notify := notifier(storage)
		for range 2 {
			require.NoError(t, notify(t.Context(), selectors.NewNameSelector[T]("0", "ns")))
		}
		require.Equal(t, 2, calls)
	})
}

// These tests do not run in parallel: metadata is package-global during construction.
func rawClient(t *testing.T, storage resource.ExternalStorage[*unstructured.UnstructuredResource], md unstructured.MetadataProvider) unstructured.UnstructuredClient {
	t.Helper()
	previous := unstructured.GetGlobalMetadataProvider()
	defer unstructured.SetGlobalMetadataProvider(previous)
	unstructured.SetGlobalMetadataProvider(md)
	remote, err := cl.NewRemoteClient[*unstructured.UnstructuredResource](storage)
	require.NoError(t, err)
	return unstructured.NewUnstructuredClient(remote, md)
}
