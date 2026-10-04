package e2e

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Redis Streams, через которые state-manager уведомляет операторов: <shard>_stream,
// группа потребителей оператора — <shard>_group (см. redis-informer-provider).

func streamName(shard string) string { return shard + "_stream" }
func groupName(shard string) string  { return shard + "_group" }

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("E2E_INFORMER_URL")})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// streamEvent — сообщение state-manager в стриме шарда.
type streamEvent struct {
	ID        string
	Type      string
	Group     string
	Kind      string
	Namespace string
	Name      string
}

func streamEvents(t *testing.T, rdb *redis.Client, shard string) []streamEvent {
	t.Helper()
	msgs, err := rdb.XRange(context.Background(), streamName(shard), "-", "+").Result()
	require.NoError(t, err)
	out := make([]streamEvent, 0, len(msgs))
	for _, m := range msgs {
		str := func(k string) string { s, _ := m.Values[k].(string); return s }
		out = append(out, streamEvent{
			ID:        m.ID,
			Type:      str("type"),
			Group:     str("resource_group"),
			Kind:      str("kind"),
			Namespace: str("namespace"),
			Name:      str("name"),
		})
	}
	return out
}

// eventsFor — события стрима по имени ресурса.
func eventsFor(events []streamEvent, name string) []streamEvent {
	var out []streamEvent
	for _, e := range events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// pendingCount — число доставленных, но не подтверждённых сообщений группы оператора.
func pendingCount(t *testing.T, rdb *redis.Client, shard string) int64 {
	t.Helper()
	p, err := rdb.XPending(context.Background(), streamName(shard), groupName(shard)).Result()
	if err != nil && strings.HasPrefix(err.Error(), "NOGROUP") {
		return 0
	}
	require.NoError(t, err)
	return p.Count
}

// xadd кладёт в стрим шарда произвольное сообщение (имитация стороннего или битого продюсера).
func xadd(t *testing.T, rdb *redis.Client, shard string, values map[string]any) string {
	t.Helper()
	id, err := rdb.XAdd(context.Background(), &redis.XAddArgs{Stream: streamName(shard), Values: values}).Result()
	require.NoError(t, err)
	return id
}
