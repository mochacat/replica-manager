// Package policy provides an optional, tamper-resistant replica-policy layer.
//
// The policy is a single Rego file baked into the container image at build
// time and compiled once at startup. Because the rules live in the image and
// not in the cluster, no in-cluster actor can modify them — this is what makes
// the layer tamper-resistant against insiders who already hold Deployment
// write permissions. See policy.rego for the rules.
//
// The package is deliberately opt-in. When disabled, it is a no-op so the
// handler and reconciler behave exactly as before.
package policy

import (
	"context"
	"embed"
	"fmt"

	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/topdown"
)

//go:embed policy.rego
var policyFS embed.FS

const (
	// policyPath is the embedded path of the Rego rule file.
	policyPath = "policy.rego"
	// resultPath is the Rego expression we evaluate for the decision.
	resultPath = "data.replicamanager.result"
)

// Input is the policy decision input for a replica request.
type Input struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Desired   int32  `json:"desired"`
	Current   int32  `json:"current"`
}

// Result is the policy decision.
type Result struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Policy compiles a Rego file once and evaluates replica requests against it.
// A nil *Policy is a valid no-op (all requests allowed) and is returned for a
// disabled policy, so callers can always use it without nil checks.
type Policy struct {
	prepared rego.PreparedEvalQuery
	enabled  bool
}

// Load compiles the embedded policy. If enabled is true, a policy that fails
// to load is returned as an error so the caller can exit fatally — we never
// want a "secure" mode silently running without a usable policy. If enabled
// is false, it returns a no-op policy and never errors.
func Load(enabled bool) (*Policy, error) {
	if !enabled {
		return &Policy{enabled: false}, nil
	}

	src, err := policyFS.ReadFile(policyPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded policy %q: %w", policyPath, err)
	}

	prepared, err := rego.New(
		rego.Query(resultPath),
		rego.Module("policy.rego", string(src)),
	).PrepareForEval(context.Background())
	if err != nil {
		return nil, fmt.Errorf("compile policy: %w", err)
	}

	return &Policy{prepared: prepared, enabled: true}, nil
}

// Evaluate decides whether a requested replica count is allowed for the given
// input. On a no-op policy it always returns allowed with no error.
func (p *Policy) Evaluate(ctx context.Context, in Input) (Result, error) {
	if p == nil || !p.enabled {
		return Result{Allowed: true}, nil
	}

	rs, err := p.prepared.Eval(ctx, rego.EvalInput(map[string]any{
		"namespace": in.Namespace,
		"name":      in.Name,
		"desired":   in.Desired,
		"current":   in.Current,
	}))
	if err != nil {
		if topdown.IsCancel(err) {
			return Result{Allowed: false}, ctx.Err() // propagate caller cancellation
		}
		return Result{Allowed: false}, fmt.Errorf("evaluate policy: %w", err)
	}

	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		// No result bound — treat as denial plus a descriptive reason.
		return Result{Allowed: false, Reason: "policy produced no decision"}, nil
	}

	raw, ok := rs[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return Result{Allowed: false, Reason: "policy result has unexpected shape"}, nil
	}

	allowed, _ := raw["allowed"].(bool)
	reason, _ := raw["reason"].(string)
	return Result{Allowed: allowed, Reason: reason}, nil
}
