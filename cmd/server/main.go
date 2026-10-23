package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/mochacat/replica-manager/controller"
	"github.com/mochacat/replica-manager/internal"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"github.com/go-logr/logr"
)

func main() {
	// TODO: Add configuration system.
	// ie environment variables for listen address,
	// cert dir, namespace filter, and log level.
	//
	// TODO: Switch to JSON logging:
	//   slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Try to load in-cluster config; fall back to local kubeconfig.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = ctrl.GetConfig()
		if err != nil {
			slog.Error("Failed to get K8s config", "error", err)
			return
		}
	}

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		// TODO: Make metrics and health probe bind addresses configurable.
	})
	if err != nil {
		slog.Error("Failed to create manager", "error", err)
		return
	}

	if err := (&controller.DeploymentReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		slog.Error("Failed to register reconciler", "error", err)
		return
	}

	// Route controller-runtime logs through slog.
	handlerOpts := slog.HandlerOptions{
		AddSource: true,
		Level:     slog.Level(-1),
	}
	log.SetLogger(logr.FromSlogHandler(slog.NewJSONHandler(os.Stderr, &handlerOpts)))

	var ready atomic.Bool

	go func() {
		slog.Info("Starting controller-runtime manager")
		if err := mgr.Start(ctx); err != nil {
			slog.Error("Manager exited", "error", err)
		}
		ready.Store(false)
	}()

	// Wait for cache sync before accepting requests.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		slog.Error("Manager cache did not sync")
		return
	}
	ready.Store(true)

	go func() {
		if err := internal.Start(ctx, mgr.GetClient(), &ready); err != nil {
			slog.Error("gRPC server exited", "error", err)
			cancel()
		}
	}()

	slog.Info("Replica manager started")

	// Block until signal received, then let defer cancel() and
	// GracefulStop clean everything up.
	<-ctx.Done()
	slog.Info("Shutting down...")
}