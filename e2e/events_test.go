package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// События state-manager в Redis Streams и их обработка информером оператора.

// TestEvents_PublishedByStateManager: create / PUT / DELETE публикуют в стрим шарда событие с
// group / kind / namespace / name; PUT /status событий не публикует.
func TestEvents_PublishedByStateManager(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	rdb := redisClient(t)
	name := uniqueName("ev")

	_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Finalizers: []string{"e2e.test/hold"}})
	require.NoError(t, err)
	_, code, err := cl.UpdateResource(name, UpdateBody{ShardID: shard, Finalizers: []string{"e2e.test/hold"}, Spec: mustJSON(map[string]any{"marker": "x"})})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	_, code, err = cl.UpdateStatus(name, UpdateStatusBody{ShardID: shard, Version: 2, CurrentVersion: 2, Finalizers: []string{"e2e.test/hold"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, cl.DeleteResource(name)) // с финализатором — update (deletion_timestamp)

	free := uniqueName("ev-free")
	createWidget(t, cl, shard, free, nil)
	require.NoError(t, cl.DeleteResource(free)) // без финализаторов — delete

	events := streamEvents(t, rdb, shard)
	var types []string
	for _, e := range eventsFor(events, name) {
		require.Equal(t, widgetGroup, e.Group)
		require.Equal(t, widgetKind, e.Kind)
		require.Equal(t, testNamespace, e.Namespace)
		types = append(types, e.Type)
	}
	require.Equal(t, []string{"update", "update", "update"}, types, "create, PUT, DELETE with finalizer; status update is silent")

	types = nil
	for _, e := range eventsFor(events, free) {
		types = append(types, e.Type)
	}
	require.Equal(t, []string{"update", "delete"}, types)
}

// TestEvents_StreamClearedOnOperatorStart: накопившиеся до старта события сбрасываются
// (ClearQueue), а сами ресурсы подхватываются через ListPending.
func TestEvents_StreamClearedOnOperatorStart(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	rdb := redisClient(t)
	names := []string{uniqueName("ev-clear"), uniqueName("ev-clear")}
	for _, n := range names {
		createWidget(t, cl, shard, n, nil)
	}
	xadd(t, rdb, shard, map[string]any{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace, "name": "ghost", "type": "update"})
	require.Len(t, streamEvents(t, rdb, shard), 3)

	startOperator(t, operatorOpts{Shard: shard})
	for _, n := range names {
		waitSynced(t, cl, n)
	}
	require.Empty(t, eventsFor(streamEvents(t, rdb, shard), "ghost"))
	require.Zero(t, pendingCount(t, rdb, shard))
}

// TestEvents_UnknownKindAcked: событие о kind, для которого нет контроллера, подтверждается и
// не мешает обработке остальных.
func TestEvents_UnknownKindAcked(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	rdb := redisClient(t)
	startOperator(t, operatorOpts{Shard: shard})

	xadd(t, rdb, shard, map[string]any{"resource_group": "other.group", "kind": "unknown", "namespace": testNamespace, "name": "x", "type": "update"})
	name := uniqueName("ev-after-unknown")
	createWidget(t, cl, shard, name, nil)
	waitSynced(t, cl, name)
	require.NoError(t, waitUntil(10*time.Second, pollStep, func() (bool, error) {
		return pendingCount(t, rdb, shard) == 0 && len(streamEvents(t, rdb, shard)) == 0, nil
	}), "unknown kind event must be acked and deleted")
}

// TestEvents_MissingResourceAcked: событие update о ресурсе, которого нет в state-manager (404),
// подтверждается без ошибок.
func TestEvents_MissingResourceAcked(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	rdb := redisClient(t)
	op := startOperator(t, operatorOpts{Shard: shard})

	xadd(t, rdb, shard, map[string]any{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace, "name": uniqueName("ghost"), "type": "update"})
	require.NoError(t, waitUntil(10*time.Second, pollStep, func() (bool, error) {
		return pendingCount(t, rdb, shard) == 0 && len(streamEvents(t, rdb, shard)) == 0, nil
	}))
	require.NotContains(t, op.Logs(), "dropped")

	name := uniqueName("ev-after-ghost")
	createWidget(t, cl, shard, name, nil)
	waitSynced(t, cl, name)
}

// TestEvents_MalformedMessage: сообщение без обязательных полей не должно останавливать
// информер — следующий ресурс обрабатывается.
//
// Известный баг redis-informer-provider: handleBatch делает m.Values[...].(string) без проверки,
// паника перехватывается в StorageInformer.Run, и Listen молча прекращает читать стрим.
func TestEvents_MalformedMessage(t *testing.T) {
	t.Parallel()
	knownBug(t, "redis-informer-provider stops listening after a message without required fields")
	shard, cl := setupShard(t)
	rdb := redisClient(t)
	startOperator(t, operatorOpts{Shard: shard})

	xadd(t, rdb, shard, map[string]any{"name": "broken", "type": "update"})
	name := uniqueName("ev-after-broken")
	createWidget(t, cl, shard, name, nil)
	waitSynced(t, cl, name)
}

// TestEvents_DeleteEventOnFinalizerRemoval: когда удаляемый ресурс исчезает из-за снятия
// последнего финализатора (PUT или PUT /status), в стрим должно уйти событие delete — как при
// DELETE ресурса без финализаторов.
//
// Известный баг state-manager: Update / UpdateStatus удаляют строку без события, оператор шарда
// не узнаёт об удалении, и объект остаётся в его памяти.
func TestEvents_DeleteEventOnFinalizerRemoval(t *testing.T) {
	t.Parallel()
	knownBug(t, "state-manager deletes a resource via PUT / PUT status without publishing a delete event")
	shard, cl := setupShard(t)
	rdb := redisClient(t)

	viaPut, viaStatus := uniqueName("ev-fin-put"), uniqueName("ev-fin-status")
	for _, n := range []string{viaPut, viaStatus} {
		_, err := cl.Create(CreateOpts{ShardID: shard, Name: n, Finalizers: []string{"e2e.test/hold"}})
		require.NoError(t, err)
		require.NoError(t, cl.DeleteResource(n))
	}
	_, code, err := cl.UpdateResource(viaPut, UpdateBody{ShardID: shard})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	_, code, err = cl.UpdateStatus(viaStatus, UpdateStatusBody{ShardID: shard, Version: 2, CurrentVersion: 2})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	events := streamEvents(t, rdb, shard)
	for _, n := range []string{viaPut, viaStatus} {
		var types []string
		for _, e := range eventsFor(events, n) {
			types = append(types, e.Type)
		}
		require.Contains(t, types, "delete", "%s: events %v", n, types)
	}
}
