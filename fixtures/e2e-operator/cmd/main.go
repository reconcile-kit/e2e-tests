package main

import (
	"context"
	"fmt"
	"log"
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

	mgr := rtm.New(cfg.ShardID, cfg.InformerURL, cfg.StorageURL, rtm.WithLogger(l))

	if err := rtm.SetController[*api.E2EWidget](mgr, controllers.NewWidgetReconciler[*api.E2EWidget](l)); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Run(ctx); err != nil {
		log.Fatal(fmt.Errorf("manager run: %w", err))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	l.Infof("shutdown signal, stopping manager")
	mgr.Stop()
	l.Infof("stopped")
}
