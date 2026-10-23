package common_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reconcile-kit/api/resource"
	listall "github.com/reconcile-kit/controlloop/common/listall"
	"github.com/stretchr/testify/require"
)

func TestListAll_Pagination(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		count   int
		offsets []int
	}{
		{"empty", 0, []int{0}}, {"short", 7, []int{0}},
		{"full_page", 500, []int{0, 500}}, {"second_page", 501, []int{0, 500}},
		{"two_full_pages", 1000, []int{0, 500, 1000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := resource.ListOpts{Name: "name", Namespace: "ns", ShardID: "shard", Limit: 2, Offset: 77,
				LabelSelectors: []resource.LabelSelector{{Key: "team", Operator: resource.LabelSelectorOperatorIn, Values: []string{"a"}}}}
			var offsets []int
			got, err := listall.ListAll(t.Context(), func(ctx context.Context, page resource.ListOpts) (map[resource.ObjectKey]int, error) {
				require.Equal(t, t.Context(), ctx)
				want := opts
				want.Limit = 500
				want.Offset = page.Offset
				require.Equal(t, want, page)
				offsets = append(offsets, page.Offset)
				items := map[resource.ObjectKey]int{}
				for i := page.Offset; i < min(tc.count, page.Offset+page.Limit); i++ {
					items[resource.ObjectKey{Namespace: "ns", Name: fmt.Sprint(i)}] = i
				}
				return items, nil
			}, opts)
			require.NoError(t, err)
			require.Equal(t, tc.offsets, offsets)
			require.Len(t, got, tc.count)
			for i := range tc.count {
				require.Equal(t, i, got[resource.ObjectKey{Namespace: "ns", Name: fmt.Sprint(i)}])
			}
			require.Equal(t, 2, opts.Limit)
			require.Equal(t, 77, opts.Offset)
		})
	}
}

func TestListAll_NoPartialResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                      string
		cancelBefore, cancelAfter bool
	}{
		{"page_error", false, false}, {"canceled_before_list", true, false}, {"canceled_between_pages", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			failure := errors.New("page unavailable")
			calls := 0
			got, err := listall.ListAll(ctx, func(_ context.Context, page resource.ListOpts) (map[resource.ObjectKey]int, error) {
				calls++
				if calls == 2 {
					return nil, failure
				}
				items := map[resource.ObjectKey]int{}
				for i := range page.Limit {
					items[resource.ObjectKey{Name: fmt.Sprint(i)}] = i
				}
				if tc.cancelAfter {
					cancel()
				}
				return items, nil
			}, resource.ListOpts{})
			require.Nil(t, got)
			if tc.cancelBefore {
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, calls)
				return
			}
			if tc.cancelAfter {
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, 1, calls)
				return
			}
			require.ErrorIs(t, err, failure)
			require.ErrorContains(t, err, "offset 500")
			require.Equal(t, 2, calls)
		})
	}
}

func TestListAll_ObjectKeysIncludeNamespace(t *testing.T) {
	t.Parallel()
	want := map[resource.ObjectKey]int{{Namespace: "one", Name: "same"}: 1, {Namespace: "two", Name: "same"}: 2}
	got, err := listall.ListAll(t.Context(), func(context.Context, resource.ListOpts) (map[resource.ObjectKey]int, error) { return want, nil }, resource.ListOpts{})
	require.NoError(t, err)
	require.Equal(t, want, got)
}
