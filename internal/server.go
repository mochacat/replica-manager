package internal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	v1 "github.com/mochacat/replica-manager/api/replicamanager/v1"
	"github.com/mochacat/replica-manager/internal/policy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Start creates and starts the gRPC server with mTLS and serves until the
// listener is closed or ctx is cancelled. It returns any terminal error so
// the caller can decide how to report it. If pol is non-nil, the server
// enforces it on SetReplicas. A nil policy (policy disabled) is a no-op.
func Start(ctx context.Context, k8sClient client.Client, ready *atomic.Bool, pol *policy.Policy) error {
	certDir := os.Getenv("CERT_DIR")
	if certDir == "" {
		return errors.New("CERT_DIR must be set")
	}
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = "0.0.0.0:8443"
	}
	healthListenAddr := os.Getenv("HEALTH_LISTEN_ADDR")
	if healthListenAddr == "" {
		// Default: port 8444 on all interfaces, no mTLS.
		healthListenAddr = "0.0.0.0:8444"
	}

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	creds, err := loadCerts(certDir)
	if err != nil {
		return fmt.Errorf("load mTLS credentials: %w", err)
	}

	s := grpc.NewServer(grpc.Creds(creds))
	rms := &handler{
		client: k8sClient,
		ready:  ready,
		policy: pol,
	}
	v1.RegisterReplicaServiceServer(s, rms)
	healthgrpc.RegisterHealthServer(s, health.NewServer())

	// Start an insecure health check listener for K8s probes.
	healthLis, err := net.Listen("tcp", healthListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", healthListenAddr, err)
	}
	healthS := grpc.NewServer()
	healthgrpc.RegisterHealthServer(healthS, health.NewServer())

	// Gracefully stop the servers when the context is cancelled.
	go func() {
		<-ctx.Done()
		slog.Info("gRPC servers shutting down...")
		s.GracefulStop()
		healthS.GracefulStop()
	}()

	slog.Info("Replica manager server listening", "addr", listenAddr, "tls", "mTLS enabled")
	// Serve the health server in the background, then block on the mTLS server.
	go healthS.Serve(healthLis)
	return s.Serve(lis)
}

func loadCerts(certDir string) (credentials.TransportCredentials, error) {
	serverCert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "server-public.pem"),
		filepath.Join(certDir, "server-private.pem"),
	)
	if err != nil {
		return nil, err
	}

	caCertPEM, err := os.ReadFile(filepath.Join(certDir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caCertPEM) {
		return nil, status.Error(codes.Internal, "failed to add CA to client cert pool")
	}

	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
	})
	return creds, nil
}
