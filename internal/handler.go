package internal

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/mochacat/replica-manager/api/replicamanager/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AnnotationKey is the annotation on Deployments that stores the desired replica count.
const AnnotationKey = "replicamanager/replicas"

// desiredFromAnnotation parses the desired replica count from a Deployment annotation.
// Returns 0, false if the annotation is missing or unparseable.
func desiredFromAnnotation(dep *appsv1.Deployment) (int32, bool) {
	if dep.Annotations == nil {
		return 0, false
	}
	val, ok := dep.Annotations[AnnotationKey]
	if !ok {
		return 0, false
	}
	desired, err := strconv.ParseInt(val, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(desired), true
}

type handler struct {
	v1.UnimplementedReplicaServiceServer
	client client.Client
	ready  *atomic.Bool
}

func (s *handler) GetReplicaCount(ctx context.Context, req *v1.GetReplicaCountRequest) (*v1.GetReplicaCountResponse, error) {
	name, namespace := req.GetName(), req.GetNamespace()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}

	var dep appsv1.Deployment
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dep); err != nil {
		return nil, status.Error(codes.NotFound, "deployment not found")
	}

	desired, _ := desiredFromAnnotation(&dep)

	return &v1.GetReplicaCountResponse{
		Namespace: namespace,
		Name:      name,
		Replica: &v1.Replica{
			Desired:   desired,
			Ready:     dep.Status.ReadyReplicas,
			Available: dep.Status.AvailableReplicas,
		},
	}, nil
}

func (s *handler) ListReplicas(ctx context.Context, req *v1.ListReplicasRequest) (*v1.ListReplicasResponse, error) {
	namespace := req.GetNamespace()
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}

	var deps appsv1.DeploymentList
	if err := s.client.List(ctx, &deps, client.InNamespace(namespace)); err != nil {
		return nil, status.Error(codes.Internal, "failed to list deployments")
	}

	var replicas []*v1.Replica
	for _, dep := range deps.Items {
		desired, _ := desiredFromAnnotation(&dep)
		replicas = append(replicas, &v1.Replica{
			Desired:   desired,
			Ready:     dep.Status.ReadyReplicas,
			Available: dep.Status.AvailableReplicas,
		})
	}

	return &v1.ListReplicasResponse{
		Namespace: namespace,
		Replicas:  replicas,
	}, nil
}

func (s *handler) SetReplicas(ctx context.Context, req *v1.SetReplicasCountRequest) (*v1.Empty, error) {
	name, namespace, desired := req.GetName(), req.GetNamespace(), req.GetDesired()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	if desired < 0 {
		return nil, status.Error(codes.InvalidArgument, "desired count must be non-negative")
	}

	var dep appsv1.Deployment
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dep); err != nil {
		return nil, status.Error(codes.NotFound, "deployment not found")
	}

	// Only patch the annotation — the reconciler will update spec.replicas.
	patch := client.MergeFrom(dep.DeepCopy())
	if dep.Annotations == nil {
		dep.Annotations = make(map[string]string)
	}
	dep.Annotations[AnnotationKey] = fmt.Sprintf("%d", desired)
	if err := s.client.Patch(ctx, &dep, patch); err != nil {
		slog.Error("failed to patch deployment annotation", "namespace", namespace, "name", name, "error", err)
		return nil, status.Error(codes.Internal, "failed to update deployment")
	}

	return &v1.Empty{}, nil
}

func (s *handler) HealthCheck(ctx context.Context, req *v1.Empty) (*v1.HealthCheckResponse, error) {
	if !s.ready.Load() {
		return &v1.HealthCheckResponse{Healthy: false}, nil
	}

	// Probe the controller-runtime cache to verify K8s connectivity.
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var dep appsv1.DeploymentList
	if err := s.client.List(checkCtx, &dep, client.Limit(1)); err != nil {
		return &v1.HealthCheckResponse{Healthy: false}, nil
	}

	return &v1.HealthCheckResponse{Healthy: true}, nil
}