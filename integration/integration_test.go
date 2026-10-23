package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/mochacat/replica-manager/api/replicamanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestIntegration(t *testing.T) {
	if os.Getenv("SKIP_INTEGRATION") != "" || os.Getenv("CERT_DIR") == "" {
		t.Skip("set CERT_DIR to run integration test")
		return
	}

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	depName := "podinfo"
	serverAddr := os.Getenv("SERVER_ADDR")
	if serverAddr == "" {
		serverAddr = "localhost:8443"
	}
	certDir := os.Getenv("CERT_DIR")
	if certDir == "" {
		// Fall back to repo-root certs via TEST_SRCDIR if available.
		certDir = filepath.Join(os.Getenv("TEST_SRCDIR"), "certs")
	}

	// --- Setup: gRPC client with mTLS ---
	clientCert, err := tls.LoadX509KeyPair(
		filepath.Join(certDir, "client-public.pem"),
		filepath.Join(certDir, "client-private.pem"),
	)
	if err != nil {
		t.Fatalf("load client cert: %s", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(certDir, "ca.pem"))
	if err != nil {
		t.Fatalf("read CA cert: %s", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatalf("failed to add CA to cert pool")
	}

	clientCreds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:     roots,
	})
	conn, err := grpc.Dial(serverAddr,
		grpc.WithTransportCredentials(clientCreds),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("grpc dial %s: %s", serverAddr, err)
	}
	defer conn.Close()
	client := v1.NewReplicaServiceClient(conn)

	// --- HealthCheck ---
	t.Run("HealthCheck", func(t *testing.T) {
		resp, err := client.HealthCheck(context.Background(), &v1.Empty{})
		if err != nil {
			t.Errorf("HealthCheck: %s", err)
		} else if !resp.Healthy {
			t.Errorf("expected healthy=true")
		}
	})

	// --- ListReplicas initial ---
	t.Run("ListReplicas initial", func(t *testing.T) {
		resp, err := client.ListReplicas(context.Background(), &v1.ListReplicasRequest{Namespace: namespace})
		if err != nil {
			t.Errorf("ListReplicas: %s", err)
		} else {
			t.Logf("found %d deployments in manager cache", len(resp.Replicas))
		}
	})

	// --- SetReplicas: desired to 3 on podinfo ---
	// First deploy podinfo via kubectl, then set desired.
	t.Run("SetReplicas to 3", func(t *testing.T) {
		_, err := client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      depName,
			Desired:   3,
		})
		if err != nil {
			t.Errorf("SetReplicas: %s", err)
		}
	})

	// --- GetReplicaCount ---
	t.Run("GetReplicaCount for podinfo", func(t *testing.T) {
		resp, err := client.GetReplicaCount(context.Background(), &v1.GetReplicaCountRequest{
			Namespace: namespace,
			Name: depName,
		})
		if err != nil {
			t.Errorf("GetReplicaCount: %s", err)
		} else if resp.Replica == nil {
			t.Errorf("GetReplicaCount returned nil Replica")
		} else {
			t.Logf("podinfo desired=%d ready=%d available=%d",
				resp.Replica.Desired, resp.Replica.Ready, resp.Replica.Available)
		}
	})

	// --- SetReplicas same count — no-op ---
	t.Run("SetReplicas same count — no-op", func(t *testing.T) {
		_, err := client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      depName,
			Desired:   3,
		})
		if err != nil {
			t.Errorf("SetReplicas no-op: %s", err)
		}
	})

	// --- mTLS rejection ---
	t.Run("mTLS rejection", func(t *testing.T) {
		anonCreds := credentials.NewTLS(&tls.Config{
			RootCAs: roots,
		})
		anonConn, err := grpc.Dial(serverAddr,
			grpc.WithTransportCredentials(anonCreds),
		)
		if err != nil {
			t.Errorf("dial without client cert: %s", err)
		} else {
			anonClient := v1.NewReplicaServiceClient(anonConn)
			_, err = anonClient.HealthCheck(context.Background(), &v1.Empty{})
			if err == nil {
				t.Error("expected HealthCheck to fail without mTLS")
			} else {
				t.Logf("mTLS rejected as expected: %s", err)
			}
			anonConn.Close()
		}
	})

	// --- SetReplicas to 0 ---
	t.Run("SetReplicas to 0", func(t *testing.T) {
		_, err := client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      depName,
			Desired:   0,
		})
		if err != nil {
			t.Errorf("SetReplicas to 0: %s", err)
		}
		// Verify via GetReplicaCount that the annotation was set to 0.
		resp, err := client.GetReplicaCount(context.Background(), &v1.GetReplicaCountRequest{
			Namespace: namespace,
			Name:      depName,
		})
		if err != nil {
			t.Errorf("GetReplicaCount after set to 0: %s", err)
		} else if resp.Replica == nil {
			t.Errorf("GetReplicaCount returned nil Replica")
		} else if resp.Replica.Desired != 0 {
			t.Errorf("expected desired=0, got %d", resp.Replica.Desired)
		} else {
			t.Logf("desired correctly set to 0")
		}
		// Restore to 3 for subsequent tests.
		_, err = client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      depName,
			Desired:   3,
		})
		if err != nil {
			t.Errorf("restore desired to 3: %s", err)
		}
	})

	// --- SetReplicas on non-existent deployment ---
	t.Run("SetReplicas non-existent deployment", func(t *testing.T) {
		_, err := client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      "does-not-exist",
			Desired:   3,
		})
		if err == nil {
			t.Error("expected NotFound error for non-existent deployment")
		} else if status.Code(err) != codes.NotFound {
			t.Errorf("expected code NotFound, got %s: %s", status.Code(err), err)
		} else {
			t.Logf("non-existent deployment correctly rejected: %s", err)
		}
	})

	// --- ListReplicas in non-existent namespace ---
	t.Run("ListReplicas non-existent namespace", func(t *testing.T) {
		resp, err := client.ListReplicas(context.Background(), &v1.ListReplicasRequest{
			Namespace: "this-namespace-does-not-exist-12345",
		})
		if err != nil {
			t.Errorf("ListReplicas on non-existent namespace: %s", err)
		} else if len(resp.Replicas) != 0 {
			t.Errorf("expected empty list, got %d items", len(resp.Replicas))
		} else {
			t.Logf("non-existent namespace correctly returns empty list")
		}
	})

	// --- Verify controller reconciled actual replicas ---
	t.Run("Controller reconciled actual replicas", func(t *testing.T) {
		// Ensure desired is set to 3 first.
		_, err := client.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
			Namespace: namespace,
			Name:      depName,
			Desired:   3,
		})
		if err != nil {
			t.Fatalf("SetReplicas to 3: %s", err)
		}

		// Use in-cluster K8s config to verify the controller applied the change.
		config, err := rest.InClusterConfig()
		if err != nil {
			t.Fatalf("in-cluster config: %s", err)
		}
		k8sClient, err := kubernetes.NewForConfig(config)
		if err != nil {
			t.Fatalf("k8s clientset: %s", err)
		}

		// Poll for up to 15s for the controller to reconcile spec.replicas.
		ctx := context.Background()
		var actualReplicas int32
		var lastErr error
		for i := 0; i < 15; i++ {
			dep, err := k8sClient.AppsV1().Deployments(namespace).Get(ctx, depName, metav1.GetOptions{})
			if err != nil {
				lastErr = fmt.Errorf("get deployment: %w", err)
				time.Sleep(1 * time.Second)
				continue
			}
			if dep.Spec.Replicas != nil {
				actualReplicas = *dep.Spec.Replicas
			}
			if actualReplicas == 3 {
				t.Logf("controller reconciled spec.replicas to %d", actualReplicas)
				return
			}
			lastErr = fmt.Errorf("spec.replicas is %d, want 3", actualReplicas)
			time.Sleep(1 * time.Second)
		}
		t.Fatalf("controller did not reconcile within timeout: %v", lastErr)
	})

	// --- External change with desired: kubectl scale should be corrected ---
	t.Run("External scale corrected by reconciler", func(t *testing.T) {
		// Desired is still 3 from the previous test. Scale to 5 via K8s API.
		config, err := rest.InClusterConfig()
		if err != nil {
			t.Fatalf("in-cluster config: %s", err)
		}
		k8sClient, err := kubernetes.NewForConfig(config)
		if err != nil {
			t.Fatalf("k8s clientset: %s", err)
		}

		ctx := context.Background()
		// Use a patch instead of get-then-update to avoid a resourceVersion
		// conflict with the reconciler (which may have patched the Deployment
		// between our Get and Update).
		patch := []byte(`{"spec":{"replicas":5}}`)
		_, err = k8sClient.AppsV1().Deployments(namespace).Patch(ctx, depName, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		if err != nil {
			t.Fatalf("scale to 5: %s", err)
		}
		t.Log("set spec.replicas to 5 via K8s API (simulating kubectl scale)")

		// The reconciler should correct it back to 3 (the annotation value).
		var lastErr error
		for i := 0; i < 15; i++ {
			dep, err := k8sClient.AppsV1().Deployments(namespace).Get(ctx, depName, metav1.GetOptions{})
			if err != nil {
				lastErr = fmt.Errorf("get deployment: %w", err)
				time.Sleep(1 * time.Second)
				continue
			}
			current := int32(1)
			if dep.Spec.Replicas != nil {
				current = *dep.Spec.Replicas
			}
			if current == 3 {
				t.Logf("reconciler corrected spec.replicas back to %d", current)
				return
			}
			lastErr = fmt.Errorf("spec.replicas is %d, want 3 (corrected back)", current)
			time.Sleep(1 * time.Second)
		}
		t.Fatalf("reconciler did not correct external scale within timeout: %v", lastErr)
	})

	// --- External change without desired: unmanaged deployment left alone ---
	// Uses the pre-provisioned 'unmanaged-test' fixture (no replicamanager/replicas
	// annotation, deployed via test/fixtures/unmanaged-deployment.yaml). We do NOT
	// create/delete Deployments here — the ServiceAccount only has
	// get/list/watch/patch/update (not create/delete), keeping the surface bounded.
	t.Run("External scale on unmanaged deployment", func(t *testing.T) {
		config, err := rest.InClusterConfig()
		if err != nil {
			t.Fatalf("in-cluster config: %s", err)
		}
		k8sClient, err := kubernetes.NewForConfig(config)
		if err != nil {
			t.Fatalf("k8s clientset: %s", err)
		}

		ctx := context.Background()
		unmanagedName := "unmanaged-test"

		// Scale it to 5 via K8s API directly (simulating kubectl scale).
		dep, err := k8sClient.AppsV1().Deployments(namespace).Get(ctx, unmanagedName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get unmanaged deployment: %s", err)
		}
		five := int32(5)
		dep.Spec.Replicas = &five
		_, err = k8sClient.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("scale unmanaged to 5: %s", err)
		}
		t.Log("set spec.replicas to 5 on unmanaged deployment")

		// The reconciler should NOT correct it — no annotation means unmanaged.
		time.Sleep(5 * time.Second)
		dep, err = k8sClient.AppsV1().Deployments(namespace).Get(ctx, unmanagedName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get unmanaged deployment: %s", err)
		}
		current := int32(1)
		if dep.Spec.Replicas != nil {
			current = *dep.Spec.Replicas
		}
		if current != 5 {
			t.Errorf("expected spec.replicas to stay at 5 (unmanaged), got %d", current)
		} else {
			t.Log("reconciler correctly left unmanaged deployment alone")
		}
	})
}