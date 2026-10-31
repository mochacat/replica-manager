package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/go-logr/logr"
	"github.com/mochacat/replica-manager/controller"
	"github.com/mochacat/replica-manager/internal"
	"github.com/mochacat/replica-manager/internal/policy"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	// Optional replica policy. It's baked into the image, so enabling it in an
	// image/deployment makes bounds tamper-resistant (nothing in-cluster can
	// modify them). Disabled by default unless REPLICA_POLICY_ENABLED is set.
	policyEnabled := os.Getenv("REPLICA_POLICY_ENABLED") == "true"
	pol, err := policy.Load(policyEnabled)
	if err != nil {
		// Fail-fast: never run a "secure" mode without a working policy.
		slog.Error("Failed to load policy; refusing to start", "error", err)
		return
	}
	if pol != nil && policyEnabled {
		slog.Info("Replica policy enabled")
	} else {
		slog.Info("Replica policy disabled (set REPLICA_POLICY_ENABLED=true to enable)")
	}

	if err := (&controller.DeploymentReconciler{
		Client: mgr.GetClient(),
		Policy: pol,
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
		if err := internal.Start(ctx, mgr.GetClient(), &ready, pol); err != nil {
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
