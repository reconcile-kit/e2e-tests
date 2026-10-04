package app

import (
	"fmt"
	"os"
	"strconv"
)

// Config из переменных окружения (удобно для e2e-подпроцесса).
type Config struct {
	ShardID     string
	StorageURL  string
	InformerURL string
	LogLevel    int
	// Token — JWT для state-manager с включённой авторизацией; пустой — без авторизации.
	Token string
	// Workers — число параллельных reconcile контроллера виджетов (0 — по умолчанию библиотеки).
	Workers int
	// ReadyFile — если задан, создаётся после mgr.Run: информер подписан, Init выполнен.
	ReadyFile string
}

func LoadConfig() (*Config, error) {
	cfg := Config{
		ShardID:     os.Getenv("E2E_SHARD_ID"),
		StorageURL:  os.Getenv("E2E_STORAGE_URL"),
		InformerURL: os.Getenv("E2E_INFORMER_URL"),
		LogLevel:    4,
		Token:       os.Getenv("E2E_OPERATOR_TOKEN"),
		ReadyFile:   os.Getenv("E2E_READY_FILE"),
	}
	if cfg.ShardID == "" {
		cfg.ShardID = "e2e-shard-1"
	}
	if cfg.StorageURL == "" {
		return nil, fmt.Errorf("E2E_STORAGE_URL is required")
	}
	if cfg.InformerURL == "" {
		return nil, fmt.Errorf("E2E_INFORMER_URL is required")
	}
	if v := os.Getenv("E2E_LOG_LEVEL"); v != "" {
		var lvl int
		_, _ = fmt.Sscanf(v, "%d", &lvl)
		if lvl > 0 {
			cfg.LogLevel = lvl
		}
	}
	if v := os.Getenv("E2E_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid E2E_WORKERS %q", v)
		}
		cfg.Workers = n
	}
	return &cfg, nil
}
