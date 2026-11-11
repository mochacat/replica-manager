// Package controller provides the reconciliation loop for Deployment replica counts.
package controller

import (
	"context"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mochacat/replica-manager/internal/policy"
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

// DeploymentReconciler watches Deployments and ensures each one matches its
// desired replica count as stored in the replicamanager/replicas annotation.
type DeploymentReconciler struct {
	client.Client
	// Policy is an optional guardrail. When non-nil, the reconciler refuses to
	// apply a desired count the policy denies — this is the tamper-proof
	// backstop against annotation edits that bypass the gRPC API. When nil,
	// policy enforcement is disabled and the reconciler behaves as before.
	Policy *policy.Policy
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch

// Reconcile compares the current Deployment spec.replicas to the desired count
// from the replicamanager/replicas annotation and patches if they differ.
func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var dep appsv1.Deployment
	if err := r.Get(ctx, req.NamespacedName, &dep); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	desired, ok := desiredFromAnnotation(&dep)
	if !ok {
		// No annotation — Deployment is not managed by us. Nothing to do.
		return ctrl.Result{}, nil
	}

	current := int32(1)
	if dep.Spec.Replicas != nil {
		current = *dep.Spec.Replicas
	}

	if current == desired {
		return ctrl.Result{}, nil
	}

	// Optional policy guardrail: if the reconcile target is one the policy
	// denies, refuse to patch spec.replicas. This is the security backstop —
	// it runs on every reconcile, so even an annotation that bypassed the gRPC
	// handler (e.g. kubectl annotate / a compromised writer) is caught here.
	// Above-max is strictly refused (never clamped) so an absurd value such as
	// 1000 never materializes; below-min is likewise refused rather than
	// force-raised (which could fight a legitimate scale-down).
	if r.Policy != nil {
		res, err := r.Policy.Evaluate(ctx, policy.Input{
			Namespace: dep.Namespace,
			Name:      dep.Name,
			Desired:   desired,
			Current:   current,
		})
		if err != nil {
			// Fail closed: do not scale without a working policy.
			log.FromContext(ctx).
				Error(err, "policy evaluation failed, refusing to reconcile Deployment",
					"namespace", dep.Namespace, "name", dep.Name, "desired", desired)
			return ctrl.Result{}, nil
		}
		if !res.Allowed {
			log.FromContext(ctx).
				Info("reconcile refused by policy (spec.replicas left untouched)",
					"namespace", dep.Namespace, "name", dep.Name,
					"desired", desired, "current", current, "reason", res.Reason)
			return ctrl.Result{}, nil
		}
	}

	patch := client.MergeFrom(dep.DeepCopy())
	dep.Spec.Replicas = &desired
	if err := r.Patch(ctx, &dep, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Complete(r)
}
