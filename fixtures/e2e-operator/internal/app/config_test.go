package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"E2E_SHARD_ID", "E2E_LOG_LEVEL", "E2E_OPERATOR_TOKEN", "E2E_WORKERS", "E2E_READY_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("E2E_STORAGE_URL", "http://sm")
	t.Setenv("E2E_INFORMER_URL", "redis:6379")
}

func TestLoadConfig_Defaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.Equal(t, &Config{
		ShardID:     "e2e-shard-1",
		StorageURL:  "http://sm",
		InformerURL: "redis:6379",
		LogLevel:    4,
	}, cfg)
}

func TestLoadConfig_AllSet(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("E2E_SHARD_ID", "s2")
	t.Setenv("E2E_LOG_LEVEL", "5")
	t.Setenv("E2E_OPERATOR_TOKEN", "tok")
	t.Setenv("E2E_WORKERS", "4")
	t.Setenv("E2E_READY_FILE", "/tmp/ready")
	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.Equal(t, "s2", cfg.ShardID)
	require.Equal(t, 5, cfg.LogLevel)
	require.Equal(t, "tok", cfg.Token)
	require.Equal(t, 4, cfg.Workers)
	require.Equal(t, "/tmp/ready", cfg.ReadyFile)
}

func TestLoadConfig_Required(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("E2E_STORAGE_URL", "")
	_, err := LoadConfig()
	require.ErrorContains(t, err, "E2E_STORAGE_URL")

	setBaseEnv(t)
	t.Setenv("E2E_INFORMER_URL", "")
	_, err = LoadConfig()
	require.ErrorContains(t, err, "E2E_INFORMER_URL")
}

// Нечисловой или неположительный уровень логов игнорируется — остаётся 4 (info).
func TestLoadConfig_LogLevelIgnoredWhenInvalid(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		setBaseEnv(t)
		t.Setenv("E2E_LOG_LEVEL", v)
		cfg, err := LoadConfig()
		require.NoError(t, err)
		require.Equal(t, 4, cfg.LogLevel, "E2E_LOG_LEVEL=%q", v)
	}
}

func TestLoadConfig_InvalidWorkers(t *testing.T) {
	for _, v := range []string{"x", "-2"} {
		setBaseEnv(t)
		t.Setenv("E2E_WORKERS", v)
		_, err := LoadConfig()
		require.ErrorContains(t, err, "E2E_WORKERS", "E2E_WORKERS=%q", v)
	}
}
