//go:build integration

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const configKind = "e2e-config"
const consumerKind = "e2e-consumer"

type commonStatus struct {
	Conditions            []struct{ Type, Status, Reason string } `json:"conditions"`
	ResolvedValue         string                                  `json:"resolvedValue"`
	ResolvedVersion       int                                     `json:"resolvedVersion"`
	ObservedValue         string                                  `json:"observedValue"`
	ObservedConfigVersion int                                     `json:"observedConfigVersion"`
}

func (s commonStatus) condition(kind, value string) bool {
	for _, c := range s.Conditions {
		if c.Type == kind {
			return c.Status == value
		}
	}
	return false
}

func waitCommon(t *testing.T, client *SMClient, kind, name string, check func(*ResourceDTO, commonStatus) bool) *ResourceDTO {
	t.Helper()
	var last *ResourceDTO
	err := waitUntil(readyTimeout, pollStep, func() (bool, error) {
		obj, code, err := client.GetKind(kind, name)
		if err != nil {
			return false, err
		}
		if code != 200 {
			return false, fmt.Errorf("get %s/%s: %d", kind, name, code)
		}
		last = obj
		var status commonStatus
		if err := json.Unmarshal(obj.Status, &status); err != nil {
			return false, err
		}
		return check(obj, status), nil
	})
	require.NoError(t, err, "%s/%s last=%+v", kind, name, last)
	return last
}

func waitConfig(t *testing.T, client *SMClient, name, value string) *ResourceDTO {
	t.Helper()
	return waitCommon(t, client, configKind, name, func(obj *ResourceDTO, status commonStatus) bool {
		return obj.Version == obj.CurrentVersion && status.ResolvedVersion == obj.Version && status.ResolvedValue == value && status.condition("DependentsNotified", "True")
	})
}

func waitConsumer(t *testing.T, client *SMClient, name, value string, configVersion int) *ResourceDTO {
	t.Helper()
	return waitCommon(t, client, consumerKind, name, func(obj *ResourceDTO, status commonStatus) bool {
		return obj.Version == obj.CurrentVersion && status.ObservedConfigVersion == configVersion && status.ObservedValue == value && status.condition("ConsumerSynced", "True")
	})
}

func commonToken(t *testing.T, ownedShard, remoteShard, remoteKind string, verbs []string) *string {
	t.Helper()
	if !authEnabled() {
		return nil
	}
	permissions := append(shardPermissions(ownedShard), map[string]any{
		"verbs": verbs, "resource_group": widgetGroup, "namespace": testNamespace, "kind": remoteKind, "shard_id": remoteShard,
	})
	return strPtr(signToken(t, tokenOpts{Subject: "common-" + ownedShard, Permissions: permissions}))
}

func createConfig(t *testing.T, client *SMClient, source, target, name, mode, selector, consumerName string) *ResourceDTO {
	t.Helper()
	obj, err := client.Create(CreateOpts{Kind: configKind, Name: name, ShardID: source, Spec: mustJSON(map[string]any{
		"value": "v1", "notify": map[string]any{"mode": mode, "targetShardID": target, "labelSelector": selector, "name": consumerName},
	})})
	require.NoError(t, err)
	return obj
}

func createConsumer(t *testing.T, client *SMClient, shard, name, config, label string, enabled, paused bool) *ResourceDTO {
	t.Helper()
	obj, err := client.Create(CreateOpts{Kind: consumerKind, Name: name, ShardID: shard,
		Spec:   mustJSON(map[string]any{"configName": config, "enabled": enabled, "paused": paused}),
		Labels: map[string]string{"config": label, "keep": "label"}, Annotations: map[string]string{"keep": "annotation"},
	})
	require.NoError(t, err)
	return obj
}

func updateConfigValue(t *testing.T, client *SMClient, obj *ResourceDTO, value string) *ResourceDTO {
	t.Helper()
	var spec map[string]any
	require.NoError(t, json.Unmarshal(obj.Spec, &spec))
	spec["value"] = value
	updated, code, err := client.putResource(client.kindResourcePath(configKind, obj.Name), UpdateBody{
		ShardID: obj.ShardID, Spec: mustJSON(spec), Labels: obj.Labels, Annotations: obj.Annotations, Finalizers: obj.Finalizers, Version: &obj.Version,
	})
	require.NoError(t, err)
	require.Equal(t, 200, code)
	return updated
}

func assertConsumerPreserved(t *testing.T, before, after *ResourceDTO) {
	t.Helper()
	require.JSONEq(t, string(before.Spec), string(after.Spec))
	require.Equal(t, before.Labels, after.Labels)
	require.Equal(t, before.Annotations, after.Annotations)
	require.Equal(t, before.Finalizers, after.Finalizers)
	require.Equal(t, before.ShardID, after.ShardID)
}

func waitAllConsumers(t *testing.T, client *SMClient, shard, value string, version, count int) []ResourceDTO {
	t.Helper()
	var last []ResourceDTO
	err := waitUntil(2*time.Minute, pollStep, func() (bool, error) {
		items, err := client.List(map[string]string{"kind": consumerKind, "shard_id": shard, "limit": "2000"})
		if err != nil {
			return false, err
		}
		last = items
		if len(items) != count {
			return false, nil
		}
		for _, item := range items {
			var status commonStatus
			if err := json.Unmarshal(item.Status, &status); err != nil {
				return false, err
			}
			if item.Version != item.CurrentVersion || status.ObservedValue != value || status.ObservedConfigVersion != version || !status.condition("ConsumerSynced", "True") {
				return false, nil
			}
		}
		return true, nil
	})
	require.NoError(t, err, "consumers count=%d, want=%d, value=%s configVersion=%d", len(last), count, value, version)
	return last
}
