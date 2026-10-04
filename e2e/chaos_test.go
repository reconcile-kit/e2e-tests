package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Отказы инфраструктуры: рестарт state-manager и Redis под работающим оператором.
// Тесты последовательные (трогают общий стенд) и идут только в фазе no-auth.

// TestChaos_StateManagerRestart: оператор переживает рестарт state-manager и обрабатывает
// ресурсы, созданные после него.
func TestChaos_StateManagerRestart(t *testing.T) {
	onceAcrossPhases(t)
	skipIfNoCompose(t)
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})
	before := uniqueName("chaos-sm-before")
	createWidget(t, cl, shard, before, nil)
	waitSynced(t, cl, before)

	compose(t, "restart", "state-manager")
	waitStateManager(t, cl)

	after := uniqueName("chaos-sm-after")
	createWidget(t, cl, shard, after, nil)
	waitSynced(t, cl, after)
}

// TestChaos_RedisRestart: после рестарта Redis информер переподключается к стриму, новые
// события доходят до оператора.
func TestChaos_RedisRestart(t *testing.T) {
	onceAcrossPhases(t)
	skipIfNoCompose(t)
	shard, cl := setupShard(t)
	startOperator(t, operatorOpts{Shard: shard})
	before := uniqueName("chaos-redis-before")
	createWidget(t, cl, shard, before, nil)
	waitSynced(t, cl, before)

	compose(t, "restart", "redis")
	rdb := redisClient(t)
	require.NoError(t, waitUntil(30*time.Second, 500*time.Millisecond, func() (bool, error) {
		return rdb.Ping(t.Context()).Err() == nil, nil
	}))

	after := uniqueName("chaos-redis-after")
	createWidget(t, cl, shard, after, nil)
	waitSynced(t, cl, after)
}

// TestChaos_EventRedeliveredAfterStateManagerOutage: событие, пришедшее, пока state-manager
// недоступен, остаётся неподтверждённым и должно быть доставлено повторно после восстановления.
//
// Известный баг runtime-manager / redis-informer-provider: resendInterval по умолчанию 1 час,
// runtime-manager его не настраивает — изменение не будет обработано до следующего рестарта.
func TestChaos_EventRedeliveredAfterStateManagerOutage(t *testing.T) {
	onceAcrossPhases(t)
	skipIfNoCompose(t)
	knownBug(t, "unacked events are redelivered only after resendInterval=1h")
	shard, cl := setupShard(t)
	op := startOperator(t, operatorOpts{Shard: shard})
	rdb := redisClient(t)
	name := uniqueName("chaos-lost")
	createWidget(t, cl, shard, name, map[string]any{"marker": "v1"})
	waitSynced(t, cl, name)

	compose(t, "stop", "state-manager")
	t.Cleanup(func() { _, _ = composeE("start", "state-manager") })
	// Меняем spec напрямую в БД (state-manager лежит) и шлём событие, как сделал бы он сам.
	psql(t, fmt.Sprintf(`UPDATE resources SET version = version + 1, spec = '{"marker":"v2"}'
		WHERE kind = '%s' AND namespace = '%s' AND name = '%s'`, widgetKind, testNamespace, name))
	xadd(t, rdb, shard, map[string]any{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace, "name": name, "type": "update"})
	require.NoError(t, waitUntil(15*time.Second, pollStep, func() (bool, error) {
		return pendingCount(t, rdb, shard) > 0, nil
	}), "event must be delivered and left unacked while state-manager is down")
	require.Eventually(t, func() bool { return strings.Contains(op.Logs(), "left unacknowledged") }, 15*time.Second, pollStep)

	compose(t, "start", "state-manager")
	waitStateManager(t, cl)
	waitWidget(t, cl, name, func(r *ResourceDTO, st widgetStatus) bool { return synced(r, st) && st.ObservedMarker == "v2" })
}
