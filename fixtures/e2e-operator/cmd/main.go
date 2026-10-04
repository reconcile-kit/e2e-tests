package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/reconcile-kit/e2e-fixture-operator/api"
	"github.com/reconcile-kit/e2e-fixture-operator/internal/app"
	"github.com/reconcile-kit/e2e-fixture-operator/internal/controllers"
	"github.com/reconcile-kit/e2e-fixture-operator/pkg/logger"
	rtm "github.com/reconcile-kit/runtime-manager"
)

func main() {
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	l := logger.New(int32(cfg.LogLevel))
	l.Infof("e2e-fixture-operator shard=%s storage=%s redis=%s", cfg.ShardID, cfg.StorageURL, cfg.InformerURL)

	opts := []rtm.Option{rtm.WithLogger(l)}
	if cfg.Token != "" {
		opts = append(opts, rtm.WithHTTPClient(&http.Client{Transport: bearerTransport{token: cfg.Token}}))
	}
	mgr := rtm.New(cfg.ShardID, cfg.InformerURL, cfg.StorageURL, opts...)

	var widgetOpts []rtm.ControllerOption
	if cfg.Workers > 0 {
		widgetOpts = append(widgetOpts, rtm.WithConcurrentWorkers(cfg.Workers))
	}
	if err := rtm.SetController[*api.E2EWidget](mgr, controllers.NewWidgetReconciler[*api.E2EWidget](l), widgetOpts...); err != nil {
		log.Fatal(err)
	}
	if err := rtm.SetController[*api.E2EGadget](mgr, controllers.NewGadgetReconciler[*api.E2EGadget](l)); err != nil {
		log.Fatal(err)
	}
	if err := rtm.SetRemoteClient[*api.E2ERemoteNote](mgr); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Подписываемся на сигналы до Run: SIGTERM во время старта не должен убить процесс.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := mgr.Run(ctx); err != nil {
		log.Fatal(fmt.Errorf("manager run: %w", err))
	}
	l.Infof("e2e-fixture-operator ready")
	if cfg.ReadyFile != "" {
		if err := os.WriteFile(cfg.ReadyFile, []byte("ready\n"), 0o644); err != nil {
			log.Fatal(fmt.Errorf("write ready file: %w", err))
		}
	}

	<-sigCh
	l.Infof("shutdown signal, stopping manager")
	mgr.Stop()
	l.Infof("stopped")
}

// bearerTransport добавляет JWT ко всем запросам в state-manager (фаза auth в scripts/e2e.sh).
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
