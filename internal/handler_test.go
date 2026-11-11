package internal

import (
	"context"
	"sync/atomic"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/google/go-cmp/cmp"
	v1 "github.com/mochacat/replica-manager/api/replicamanager/v1"
	"github.com/mochacat/replica-manager/internal/policy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
)

func newDeployment(ns, name string, replicas int32, desiredAnnot string, ready, available int32) *appsv1.Deployment {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ns,
			Name:        name,
			Annotations: map[string]string{},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "app", Image: "nginx"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     ready,
			AvailableReplicas: available,
		},
	}
	if desiredAnnot != "" {
		dep.Annotations[AnnotationKey] = desiredAnnot
	}
	return dep
}

func TestHandler_GetReplicaCount(t *testing.T) {
	tests := []struct {
		name     string
		objs     []runtime.Object
		req      *v1.GetReplicaCountRequest
		expected *v1.GetReplicaCountResponse
		code     codes.Code
	}{
		{
			name: "missing namespace",
			req:  &v1.GetReplicaCountRequest{Name: "foo"},
			code: codes.InvalidArgument,
		},
		{
			name: "missing name",
			req:  &v1.GetReplicaCountRequest{Namespace: "foo"},
			code: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &v1.GetReplicaCountRequest{Namespace: "foo", Name: "bar"},
			code: codes.NotFound,
		},
		{
			name: "found with annotation",
			objs: []runtime.Object{newDeployment("foo", "bar", 3, "5", 2, 1)},
			req:  &v1.GetReplicaCountRequest{Namespace: "foo", Name: "bar"},
			expected: &v1.GetReplicaCountResponse{
				Namespace: "foo",
				Name:      "bar",
				Replica: &v1.Replica{
					Desired:   5,
					Ready:     2,
					Available: 1,
				},
			},
			code: codes.OK,
		},
		{
			name: "found without annotation",
			objs: []runtime.Object{newDeployment("foo", "bar", 3, "", 1, 1)},
			req:  &v1.GetReplicaCountRequest{Namespace: "foo", Name: "bar"},
			expected: &v1.GetReplicaCountResponse{
				Namespace: "foo",
				Name:      "bar",
				Replica: &v1.Replica{
					Desired:   0,
					Ready:     1,
					Available: 1,
				},
			},
			code: codes.OK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithRuntimeObjects(tt.objs...).Build()
			ready := &atomic.Bool{}
			ready.Store(true)
			h := &handler{client: fakeClient, ready: ready}
			got, err := h.GetReplicaCount(context.Background(), tt.req)

			if status.Code(err) != tt.code {
				t.Fatalf("expected code %v, got %v", tt.code, status.Code(err))
			}
			if tt.code == codes.OK {
				if diff := cmp.Diff(tt.expected, got, protocmp.Transform()); diff != "" {
					t.Fatalf("mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestHandler_ListReplicas(t *testing.T) {
	tests := []struct {
		name     string
		objs     []runtime.Object
		req      *v1.ListReplicasRequest
		expected *v1.ListReplicasResponse
		code     codes.Code
	}{
		{
			name: "missing namespace",
			req:  &v1.ListReplicasRequest{},
			code: codes.InvalidArgument,
		},
		{
			name:     "empty namespace",
			objs:     []runtime.Object{},
			req:      &v1.ListReplicasRequest{Namespace: "foo"},
			expected: &v1.ListReplicasResponse{Namespace: "foo", Replicas: []*v1.Replica{}},
			code:     codes.OK,
		},
		{
			name: "lists all in namespace, excludes other namespaces",
			objs: []runtime.Object{
				newDeployment("foo", "a", 1, "1", 1, 1),
				newDeployment("foo", "b", 2, "3", 0, 0),
				newDeployment("other", "z", 3, "", 3, 1),
			},
			req: &v1.ListReplicasRequest{Namespace: "foo"},
			expected: &v1.ListReplicasResponse{
				Namespace: "foo",
				Replicas: []*v1.Replica{
					{Desired: 1, Ready: 1, Available: 1},
					{Desired: 3, Ready: 0, Available: 0},
				},
			},
			code: codes.OK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithRuntimeObjects(tt.objs...).Build()
			ready := &atomic.Bool{}
			ready.Store(true)
			h := &handler{client: fakeClient, ready: ready}
			got, err := h.ListReplicas(context.Background(), tt.req)

			if status.Code(err) != tt.code {
				t.Fatalf("expected code %v, got %v", tt.code, status.Code(err))
			}
			if tt.code == codes.OK {
				if diff := cmp.Diff(tt.expected, got, protocmp.Transform()); diff != "" {
					t.Fatalf("mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestHandler_SetReplicas(t *testing.T) {
	tests := []struct {
		name         string
		objs         []runtime.Object
		req          *v1.SetReplicasCountRequest
		expected     *v1.Empty
		code         codes.Code
		wantAnnot    func(t *testing.T, c client.Client)
		wantReplicas func(t *testing.T, c client.Client)
	}{
		{
			name: "missing name",
			req:  &v1.SetReplicasCountRequest{Namespace: "foo", Desired: 3},
			code: codes.InvalidArgument,
		},
		{
			name: "missing namespace",
			req:  &v1.SetReplicasCountRequest{Name: "bar", Desired: 3},
			code: codes.InvalidArgument,
		},
		{
			name: "negative desired",
			req:  &v1.SetReplicasCountRequest{Namespace: "foo", Name: "bar", Desired: -3},
			code: codes.InvalidArgument,
		},
		{
			name: "deployment not found",
			req:  &v1.SetReplicasCountRequest{Namespace: "foo", Name: "bar", Desired: 3},
			code: codes.NotFound,
		},
		{
			name:     "set desired via annotation",
			objs:     []runtime.Object{newDeployment("foo", "bar", 1, "", 1, 1)},
			req:      &v1.SetReplicasCountRequest{Namespace: "foo", Name: "bar", Desired: 5},
			expected: &v1.Empty{},
			code:     codes.OK,
			wantAnnot: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				if dep.Annotations[AnnotationKey] != "5" {
					t.Fatalf("expected annotation %s=5, got %s", AnnotationKey, dep.Annotations[AnnotationKey])
				}
			},
			wantReplicas: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				// The handler only sets the annotation — spec.replicas should be unchanged.
				if *dep.Spec.Replicas != 1 {
					t.Fatalf("expected spec.replicas=1, got %d", *dep.Spec.Replicas)
				}
			},
		},
		{
			name:     "set desired to 0",
			objs:     []runtime.Object{newDeployment("foo", "bar", 2, "", 2, 2)},
			req:      &v1.SetReplicasCountRequest{Namespace: "foo", Name: "bar", Desired: 0},
			expected: &v1.Empty{},
			code:     codes.OK,
			wantAnnot: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				if dep.Annotations[AnnotationKey] != "0" {
					t.Fatalf("expected annotation %s=0, got %s", AnnotationKey, dep.Annotations[AnnotationKey])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithRuntimeObjects(tt.objs...).Build()
			ready := &atomic.Bool{}
			ready.Store(true)
			h := &handler{client: fakeClient, ready: ready}
			got, err := h.SetReplicas(context.Background(), tt.req)

			if status.Code(err) != tt.code {
				t.Fatalf("expected code %v, got %v", tt.code, status.Code(err))
			}
			if tt.code == codes.OK {
				if diff := cmp.Diff(tt.expected, got, protocmp.Transform()); diff != "" {
					t.Fatalf("mismatch (-want +got):\n%s", diff)
				}
				if tt.wantAnnot != nil {
					tt.wantAnnot(t, fakeClient)
				}
				if tt.wantReplicas != nil {
					tt.wantReplicas(t, fakeClient)
				}
			}
		})
	}
}

func TestHandler_SetReplicasPolicyDenied(t *testing.T) {
	pol, err := policy.Load(true) // baked-in default: min 1, max 20
	if err != nil {
		t.Fatalf("load policy: %s", err)
	}

	cases := []struct {
		name     string
		desired  int32
		wantCode codes.Code
		// wantAnnotation true means the request should have been accepted and
		// written the annotation; false means denied with no write.
	}{
		{"within bounds accepted", 10, codes.OK},
		{"1000 with max 20 denied (FailedPrecondition)", 1000, codes.FailedPrecondition},
		{"below min denied", 0, codes.FailedPrecondition},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithRuntimeObjects(newDeployment("foo", "bar", 1, "", 1, 1)).
				Build()
			ready := &atomic.Bool{}
			ready.Store(true)
			h := &handler{client: fakeClient, ready: ready, policy: pol}

			_, err := h.SetReplicas(context.Background(), &v1.SetReplicasCountRequest{
				Namespace: "foo",
				Name:      "bar",
				Desired:   tc.desired,
			})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("expected code %v, got %v (%v)", tc.wantCode, status.Code(err), err)
			}

			// Verify whether the annotation was written.
			var dep appsv1.Deployment
			if err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
				t.Fatal(err)
			}
			_, gotAnnot := dep.Annotations[AnnotationKey]
			if tc.wantCode == codes.OK && !gotAnnot {
				t.Fatalf("expected annotation to be written, but it was not")
			}
			if tc.wantCode == codes.FailedPrecondition && gotAnnot {
				t.Fatalf("denied request must not write the annotation")
			}
		})
	}
}

func TestHandler_HealthCheck(t *testing.T) {
	tests := []struct {
		name     string
		req      *v1.Empty
		expected *v1.HealthCheckResponse
		code     codes.Code
	}{
		{
			name:     "always healthy",
			req:      &v1.Empty{},
			expected: &v1.HealthCheckResponse{Healthy: true},
			code:     codes.OK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := &atomic.Bool{}
			ready.Store(true)
			h := &handler{client: fake.NewClientBuilder().Build(), ready: ready}
			got, err := h.HealthCheck(context.Background(), tt.req)

			if status.Code(err) != tt.code {
				t.Fatalf("expected code %v, got %v", tt.code, status.Code(err))
			}
			if tt.code == codes.OK {
				if diff := cmp.Diff(tt.expected, got, protocmp.Transform()); diff != "" {
					t.Fatalf("mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
