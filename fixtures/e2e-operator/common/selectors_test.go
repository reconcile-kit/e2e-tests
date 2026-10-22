package common_test

import (
	"testing"

	"github.com/reconcile-kit/api/resource"
	"github.com/reconcile-kit/controlloop/common/watchedby/selectors"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/stretchr/testify/require"
)

func TestSelectors_SelectionAndLocalPredicates(t *testing.T) {
	t.Parallel()
	labels := []resource.LabelSelector{{Key: "config", Operator: resource.LabelSelectorOperatorIn, Values: []string{"a"}}}
	for _, tc := range []struct {
		name     string
		selector selectors.Selector[*api.E2EConsumer]
		opts     resource.ListOpts
	}{
		{"name", selectors.NewNameSelector[*api.E2EConsumer]("consumer", "ns"), resource.ListOpts{Name: "consumer", Namespace: "ns"}},
		{"labels", selectors.NewLabelSelector[*api.E2EConsumer](labels, "ns"), resource.ListOpts{Namespace: "ns", LabelSelectors: labels}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.opts, tc.selector.GetListOpts())
			items := []*api.E2EConsumer{
				{Spec: api.E2EConsumerSpec{Enabled: true}},
				{Spec: api.E2EConsumerSpec{Enabled: true, Paused: true}},
				{Spec: api.E2EConsumerSpec{Enabled: false}},
				{Spec: api.E2EConsumerSpec{Enabled: false, Paused: true}},
			}
			// Backend selection is intentionally not repeated locally: these objects
			// have no matching name/labels, yet pass when no predicates are installed.
			require.Equal(t, items, tc.selector.FilterResources(items))
			require.Empty(t, tc.selector.FilterResources(nil))
			require.Same(t, tc.selector, tc.selector.WithFilters())
			tc.selector.WithFilters(func(c *api.E2EConsumer) bool { return c.Spec.Enabled })
			tc.selector.WithFilters(func(c *api.E2EConsumer) bool { return !c.Spec.Paused })
			require.Equal(t, items[:1], tc.selector.FilterResources(items))
			require.Equal(t, tc.opts, tc.selector.GetListOpts())
			require.Len(t, items, 4, "filtering must not alter the input slice")
		})
	}
}

func TestSelectors_ConstructorOptions(t *testing.T) {
	t.Parallel()
	filter := selectors.WithFiltersSelectorOpts[*api.E2EConsumer](func(c *api.E2EConsumer) bool { return c.Spec.Enabled })
	empty := selectors.WithFiltersSelectorOpts[*api.E2EConsumer]()
	for _, selector := range []selectors.Selector[*api.E2EConsumer]{
		selectors.NewNameSelector[*api.E2EConsumer]("a", "ns", empty, filter),
		selectors.NewLabelSelector[*api.E2EConsumer](nil, "ns", empty, filter),
	} {
		items := []*api.E2EConsumer{{Spec: api.E2EConsumerSpec{Enabled: true}}, {}}
		require.Equal(t, items[:1], selector.FilterResources(items))
	}
}
