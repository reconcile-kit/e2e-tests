//go:build integration

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestE2E_CommonWatchedBy(t *testing.T) {
	for _, mode := range []string{"structured", "unstructured"} {
		for _, selection := range []string{"labels", "name"} {
			t.Run(mode+"/"+selection, func(t *testing.T) {
				t.Parallel()
				source, client := setupShard(t)
				target := newShard()
				configName, chosen := uniqueName("config"), uniqueName("consumer")
				selector, name := "config in ("+configName+")", ""
				if selection == "name" {
					selector, name = "", chosen
				}
				createConfig(t, client, source, target, configName, mode, selector, name)
				startOperator(t, operatorOpts{Shard: source, Token: commonToken(t, source, target, consumerKind, []string{"get", "list", "update"})})
				config := waitConfig(t, client, configName, "v1") // Empty selection is successful.
				createConsumer(t, client, target, chosen, configName, configName, true, false)
				var excluded []string
				for _, tc := range []struct {
					name, label, shard string
					enabled, paused    bool
				}{
					{"disabled", configName, target, false, false}, {"paused", configName, target, true, true},
					{"other-label", "unrelated", target, true, false}, {"source-shard", configName, source, true, false},
				} {
					n := uniqueName(tc.name)
					excluded = append(excluded, n)
					createConsumer(t, client, tc.shard, n, configName, tc.label, tc.enabled, tc.paused)
				}
				// Same name in another namespace and another kind must not be selected.
				otherNS := client.WithScope(widgetGroup, uniqueName("ns"))
				if authEnabled() {
					otherNS = otherNS.WithToken(signToken(t, tokenOpts{Permissions: shardPermissions(target)}))
				}
				decoy := createConsumer(t, otherNS, target, chosen, configName, configName, true, false)
				// Mark this decoy processed before startup. It has no dependency in its
				// namespace, and must remain dormant unless the notifier selects it wrongly.
				_, code, err := otherNS.putResource(otherNS.kindResourcePath(consumerKind, chosen)+"/status", UpdateStatusBody{
					ShardID: target, Version: decoy.Version, CurrentVersion: decoy.Version, Labels: decoy.Labels, Annotations: decoy.Annotations,
				})
				require.NoError(t, err)
				require.Equal(t, 200, code)
				otherKind, err := client.Create(CreateOpts{Kind: gadgetKind, Name: chosen, ShardID: target, Spec: mustJSON(map[string]string{"marker": "keep"}), Labels: map[string]string{"config": configName}})
				require.NoError(t, err)
				startOperator(t, operatorOpts{Shard: target, Token: commonToken(t, target, source, configKind, []string{"get"})})
				baseline := waitConsumer(t, client, chosen, "v1", config.Version)
				before := map[string]*ResourceDTO{}
				for _, n := range excluded {
					before[n] = waitConsumer(t, client, n, "v1", config.Version)
				}
				for _, value := range []string{"v2", "v3"} {
					config = updateConfigValue(t, client, config, value)
					config = waitConfig(t, client, configName, value)
					after := waitConsumer(t, client, chosen, value, config.Version)
					require.Greater(t, after.Version, baseline.Version, "unchanged spec must still wake the dependent")
					assertConsumerPreserved(t, baseline, after)
				}
				requireNever(t, time.Second, func() bool {
					for n, old := range before {
						now, code, err := client.GetKind(consumerKind, n)
						require.NoError(t, err)
						require.Equal(t, 200, code)
						if now.Version != old.Version {
							return true
						}
					}
					return false
				}, "excluded consumers were notified")
				now, code, err := otherNS.GetKind(consumerKind, chosen)
				require.NoError(t, err)
				require.Equal(t, 200, code)
				require.Equal(t, decoy.Version, now.Version)
				now, code, err = client.GetKind(gadgetKind, chosen)
				require.NoError(t, err)
				require.Equal(t, 200, code)
				require.Equal(t, otherKind.Version, now.Version)
			})
		}
	}
}

func TestE2E_CommonWatchedByPagination(t *testing.T) {
	for _, mode := range []string{"structured", "unstructured"} {
		t.Run(mode, func(t *testing.T) {
			// Full paging/reconcile in both auth modes; each subtest has its own shards.
			source, client := setupShard(t)
			target := newShard()
			name := uniqueName("config-pages")
			createConfig(t, client, source, target, name, mode, "config in ("+name+")", "")
			startOperator(t, operatorOpts{Shard: source, Token: commonToken(t, source, target, consumerKind, []string{"get", "list", "update"})})
			config := waitConfig(t, client, name, "v1")
			const count = 501
			for i := range count {
				createConsumer(t, client, target, fmt.Sprintf("%s-%04d", name, i), name, name, true, false)
			}
			startOperator(t, operatorOpts{Shard: target, Token: commonToken(t, target, source, configKind, []string{"get"})})
			before := waitAllConsumers(t, client, target, "v1", config.Version, count)
			versions := map[string]int{}
			for _, obj := range before {
				versions[obj.Name] = obj.Version
			}
			config = updateConfigValue(t, client, config, "v2")
			config = waitConfig(t, client, name, "v2")
			after := waitAllConsumers(t, client, target, "v2", config.Version, count)
			for _, obj := range after {
				require.Greater(t, obj.Version, versions[obj.Name], obj.Name)
			}
		})
	}
}

func TestE2E_CommonWatchedByOfflineConsumer(t *testing.T) {
	for _, mode := range []string{"structured", "unstructured"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			source, client := setupShard(t)
			target := newShard()
			name, consumer := uniqueName("config-offline"), uniqueName("consumer-offline")
			createConfig(t, client, source, target, name, mode, "", consumer)
			startOperator(t, operatorOpts{Shard: source, Token: commonToken(t, source, target, consumerKind, []string{"get", "list", "update"})})
			config := waitConfig(t, client, name, "v1")
			created := createConsumer(t, client, target, consumer, name, name, true, false)
			config = updateConfigValue(t, client, config, "v2")
			config = waitConfig(t, client, name, "v2")
			pending, code, err := client.GetKind(consumerKind, consumer)
			require.NoError(t, err)
			require.Equal(t, 200, code)
			require.Greater(t, pending.Version, created.Version)
			require.Less(t, pending.CurrentVersion, pending.Version, "notifier success does not wait for reconcile")
			startOperator(t, operatorOpts{Shard: target, Token: commonToken(t, target, source, configKind, []string{"get"})})
			waitConsumer(t, client, consumer, "v2", config.Version)
			// Each controller owns only its finalizer, not the referenced resource.
			code, _, err = client.Do(http.MethodDelete, client.kindResourcePath(consumerKind, consumer), nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusNoContent, code)
			require.NoError(t, waitUntil(readyTimeout, pollStep, func() (bool, error) { _, code, err := client.GetKind(consumerKind, consumer); return code == 404, err }))
			code, _, err = client.Do(http.MethodDelete, client.kindResourcePath(configKind, name), nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusNoContent, code)
			require.NoError(t, waitUntil(readyTimeout, pollStep, func() (bool, error) { _, code, err := client.GetKind(configKind, name); return code == 404, err }))
		})
	}
}
