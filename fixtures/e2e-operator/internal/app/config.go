package app

import (
	"fmt"
	"os"
)

// Config из переменных окружения (удобно для e2e-подпроцесса).
type Config struct {
	ShardID     string
	StorageURL  string
	InformerURL string
	LogLevel    int
}

func LoadConfig() (*Config, error) {
	cfg := Config{
		ShardID:     os.Getenv("E2E_SHARD_ID"),
		StorageURL:  os.Getenv("E2E_STORAGE_URL"),
		InformerURL: os.Getenv("E2E_INFORMER_URL"),
		LogLevel:    4,
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
	return &cfg, nil
}
