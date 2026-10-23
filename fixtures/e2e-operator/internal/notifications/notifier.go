package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/reconcile-kit/api/resource"
	cl "github.com/reconcile-kit/controlloop"
	unstructured "github.com/reconcile-kit/controlloop/common/unstructured_resource"
	"github.com/reconcile-kit/controlloop/common/watchedby/selectors"
	"github.com/reconcile-kit/controlloop/common/watchedby/structured"
	unstructuredwatch "github.com/reconcile-kit/controlloop/common/watchedby/unstructured"
	"github.com/reconcile-kit/e2e-fixture-operator/api"
	provider "github.com/reconcile-kit/state-manager-provider"
)

// clientMu protects the whole global-metadata + remote-client construction sequence.
var clientMu sync.Mutex

type Notifier struct {
	typed *cl.RemoteClient[*api.E2EConsumer]
	raw   resource.ExternalStorage[*unstructured.UnstructuredResource]
}

func NewNotifier(address string, httpClient *http.Client) (*Notifier, error) {
	typed, err := provider.NewStateManagerProvider[*api.E2EConsumer](address, httpClient)
	if err != nil {
		return nil, err
	}
	remote, err := cl.NewRemoteClient[*api.E2EConsumer](typed)
	if err != nil {
		return nil, err
	}
	raw, err := provider.NewStateManagerProvider[*unstructured.UnstructuredResource](address, httpClient)
	if err != nil {
		return nil, err
	}
	return &Notifier{typed: remote, raw: raw}, nil
}

func (n *Notifier) Notify(ctx context.Context, config *api.E2EConfig) error {
	if config.Spec.Notify.Mode == api.NotifyUnstructured {
		return n.notifyUnstructured(ctx, config)
	}
	selector, err := newSelector[*api.E2EConsumer](config)
	if err != nil {
		return err
	}
	selector.WithFilters(
		func(c *api.E2EConsumer) bool { return c.Spec.Enabled },
		func(c *api.E2EConsumer) bool { return !c.Spec.Paused },
	)
	return structured.NewNotifier(n.typed, config.Spec.Notify.TargetShardID).NotifyWatchedByResources(ctx, selector)
}

func (n *Notifier) notifyUnstructured(ctx context.Context, config *api.E2EConfig) error {
	client, err := n.unstructuredClient(config.Spec.Notify.TargetShardID)
	if err != nil {
		return err
	}
	selector, err := newSelector[*unstructured.UnstructuredResource](config)
	if err != nil {
		return err
	}
	// The sender only knows these selection fields, not the target's Go resource type.
	selector.WithFilters(
		func(c *unstructured.UnstructuredResource) bool { return rawFlag(c.Spec, "enabled") },
		func(c *unstructured.UnstructuredResource) bool { return !rawFlag(c.Spec, "paused") },
	)
	return unstructuredwatch.NewNotifier(client).NotifyWatchedByResources(ctx, selector)
}

func (n *Notifier) unstructuredClient(shard string) (unstructured.UnstructuredClient, error) {
	clientMu.Lock()
	defer clientMu.Unlock()
	previous := unstructured.GetGlobalMetadataProvider()
	defer unstructured.SetGlobalMetadataProvider(previous)
	md := unstructured.MetadataProvider{Group: api.E2EWidgetGroup, Kind: api.E2EConsumerKind, ShardID: shard}
	unstructured.SetGlobalMetadataProvider(md)
	remote, err := cl.NewRemoteClient[*unstructured.UnstructuredResource](n.raw)
	if err != nil {
		return nil, err
	}
	return unstructured.NewUnstructuredClient(remote, md), nil
}

func newSelector[T resource.Object[T]](config *api.E2EConfig) (selectors.Selector[T], error) {
	if config.Spec.Notify.Name != "" {
		return selectors.NewNameSelector[T](config.Spec.Notify.Name, config.Namespace), nil
	}
	labels, err := resource.ParseLabelSelectors(config.Spec.Notify.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("notification selector: %w", err)
	}
	return selectors.NewLabelSelector[T](labels, config.Namespace), nil
}

func rawFlag(raw json.RawMessage, key string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	var value bool
	return json.Unmarshal(fields[key], &value) == nil && value
}
