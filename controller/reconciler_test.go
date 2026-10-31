package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mochacat/replica-manager/internal/policy"
)

func newDeployment(ns, name string, replicas int32, desiredAnnot string) *appsv1.Deployment {
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
	}
	if desiredAnnot != "" {
		dep.Annotations[AnnotationKey] = desiredAnnot
	}
	return dep
}

func TestDesiredFromAnnotation(t *testing.T) {
	tests := []struct {
		name   string
		annot  map[string]string
		want   int32
		wantOK bool
	}{
		{
			name:   "nil annotations",
			annot:  nil,
			want:   0,
			wantOK: false,
		},
		{
			name:   "missing key",
			annot:  map[string]string{"other": "5"},
			want:   0,
			wantOK: false,
		},
		{
			name:   "valid value",
			annot:  map[string]string{AnnotationKey: "5"},
			want:   5,
			wantOK: true,
		},
		{
			name:   "non-numeric value",
			annot:  map[string]string{AnnotationKey: "abc"},
			want:   0,
			wantOK: false,
		},
		{
			name:   "negative value",
			annot:  map[string]string{AnnotationKey: "-1"},
			want:   -1,
			wantOK: true,
		},
		{
			name:   "zero value",
			annot:  map[string]string{AnnotationKey: "0"},
			want:   0,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dep := &appsv1.Deployment{}
			dep.Annotations = tt.annot
			got, ok := desiredFromAnnotation(dep)
			if ok != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, ok)
			}
			if got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestDeploymentReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name         string
		objs         []runtime.Object
		req          ctrl.Request
		wantReplicas func(t *testing.T, c client.Client)
		wantResult   ctrl.Result
		wantErr      bool
	}{
		{
			name:       "deployment not found — no-op",
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "missing"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
		},
		{
			name:       "no annotation — no-op",
			objs:       []runtime.Object{newDeployment("foo", "bar", 3, "")},
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
		},
		{
			name:       "annotation matches spec — no-op",
			objs:       []runtime.Object{newDeployment("foo", "bar", 5, "5")},
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
		},
		{
			name:       "annotation differs from spec — patches",
			objs:       []runtime.Object{newDeployment("foo", "bar", 3, "5")},
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
			wantReplicas: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				if *dep.Spec.Replicas != 5 {
					t.Fatalf("expected spec.replicas=5, got %d", *dep.Spec.Replicas)
				}
			},
		},
		{
			name:       "annotation wants zero — patches to zero",
			objs:       []runtime.Object{newDeployment("foo", "bar", 3, "0")},
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
			wantReplicas: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				if *dep.Spec.Replicas != 0 {
					t.Fatalf("expected spec.replicas=0, got %d", *dep.Spec.Replicas)
				}
			},
		},
		{
			name:       "malformed annotation — no-op",
			objs:       []runtime.Object{newDeployment("foo", "bar", 3, "abc")},
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
		},
		{
			name: "spec.replicas is nil — defaults to 1 and patches",
			objs: func() []runtime.Object {
				dep := newDeployment("foo", "bar", 0, "3")
				dep.Spec.Replicas = nil
				return []runtime.Object{dep}
			}(),
			req:        ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"}},
			wantResult: ctrl.Result{},
			wantErr:    false,
			wantReplicas: func(t *testing.T, c client.Client) {
				var dep appsv1.Deployment
				if err := c.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &dep); err != nil {
					t.Fatal(err)
				}
				if *dep.Spec.Replicas != 3 {
					t.Fatalf("expected spec.replicas=3, got %d", *dep.Spec.Replicas)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithRuntimeObjects(tt.objs...).Build()
			r := &DeploymentReconciler{Client: fakeClient}
			result, err := r.Reconcile(context.Background(), tt.req)

			if (err != nil) != tt.wantErr {
				t.Fatalf("expected err=%v, got %v", tt.wantErr, err)
			}
			if result != tt.wantResult {
				t.Fatalf("expected result=%v, got %v", tt.wantResult, result)
			}
			if tt.wantReplicas != nil {
				tt.wantReplicas(t, fakeClient)
			}
		})
	}
}

// TestDeploymentReconciler_PolicyBackstop verifies the tamper-proof backstop:
// when policy is enabled, a reconciler must refuse to apply an annotation value
// the policy denies (e.g. 1000 vs a max of 20), leaving spec.replicas untouched.
// This simulates an insider editing the annotation directly, bypassing the gRPC API.
func TestDeploymentReconciler_PolicyBackstop(t *testing.T) {
	pol, err := policy.Load(true) // baked-in default: min 1, max 20
	if err != nil {
		t.Fatalf("load policy: %s", err)
	}

	dep := newDeployment("foo", "bar", 3, "1000") // tampered annotation claims 1000
	fakeClient := fake.NewClientBuilder().WithRuntimeObjects(dep).Build()
	r := &DeploymentReconciler{Client: fakeClient, Policy: pol}

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"},
	})
	if err != nil {
		t.Fatalf("reconcile unexpectedly errored: %s", err)
	}

	var got appsv1.Deployment
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Replicas == nil {
		t.Fatal("spec.replicas is nil")
	}
	if *got.Spec.Replicas != 3 {
		t.Fatalf("policy backstop failed: spec.replicas was changed to %d (expected it to stay 3)", *got.Spec.Replicas)
	}
	t.Logf("reconciler refused to scale %d (annotation %q left, spec untouched)", *got.Spec.Replicas, got.Annotations[AnnotationKey])
}

// TestDeploymentReconciler_PolicyAllowsScale verifies a within-policy reconcile
// still applies when policy is enabled.
func TestDeploymentReconciler_PolicyAllowsScale(t *testing.T) {
	pol, err := policy.Load(true)
	if err != nil {
		t.Fatalf("load policy: %s", err)
	}

	dep := newDeployment("foo", "bar", 3, "10") // within bounds
	fakeClient := fake.NewClientBuilder().WithRuntimeObjects(dep).Build()
	r := &DeploymentReconciler{Client: fakeClient, Policy: pol}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "foo", Name: "bar"},
	}); err != nil {
		t.Fatalf("reconcile errored: %s", err)
	}

	var got appsv1.Deployment
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Namespace: "foo", Name: "bar"}, &got); err != nil {
		t.Fatal(err)
	}
	if *got.Spec.Replicas != 10 {
		t.Fatalf("expected spec.replicas=10, got %d", *got.Spec.Replicas)
	}
}
