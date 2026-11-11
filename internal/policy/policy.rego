# replicamanager replica policy.
#
# This file is BAKED INTO THE CONTAINER IMAGE at build time and compiled once
# at startup. Nothing in the cluster can modify it, which is the security
# property: the bounds the controller trusts are not stored in the cluster.
#
# The gRPC handler and the reconciler both evaluate the same policy, so there
# is a single source of truth for "what replica counts are allowed."
#
# Input document (provided by the service for each evaluation):
#   {
#     "namespace": "prod",
#     "name":      "payments",
#     "desired":   12,    // the replica count being requested or claimed
#     "current":   10     // the current spec.replicas
#   }
#
# Decision: result.allowed (bool). When denied, result.reason describes why.
#
# Editing this file requires a rebuild + redeploy (by design) — it is baked
# into the image so no in-cluster actor can tamper with the bounds.

package replicamanager

import future.keywords.if
import future.keywords.in

# --------------------------------------------------------------------------
# Bounds (single global policy)
# --------------------------------------------------------------------------

# Default bounds applied to every workload/namespace the controller manages.
# For a per-namespace deviation, add a deny rule below (see the commented
# example) — this keeps the policy a single immutable global file.
default_min := 1
default_max := 20

# --------------------------------------------------------------------------
# Rules
# --------------------------------------------------------------------------

# Deny if the desired count exceeds the global max.
# This is the typo-guard: a value far above the cap (e.g. 1000 vs max 20) is
# refused outright — we never clamp, so a mis-typed count scales nothing.
deny[reason] {
    default_max < input.desired
    reason := sprintf(
        "desired %d exceeds max %d for namespace %q (refused, not clamped)",
        [input.desired, default_max, input.namespace],
    )
}

# Deny if the desired count is below the global min.
# The handler refuses; the reconciler refuses but does NOT force replicas back
# up (to avoid fighting a legitimate emergency scale-down).
deny[reason] {
    input.desired < default_min
    reason := sprintf(
        "desired %d is below min %d for namespace %q",
        [input.desired, default_min, input.namespace],
    )
}

# Per-namespace override example (uncomment and adapt to tighten/raise a bound):
# deny[reason] {
#     input.namespace == "prod"
#     input.desired > 40
#     reason := sprintf("prod workloads are capped at 40 (got %d)", [input.desired])
# }

# A desired count is allowed only if no rule denies it.
allow := true if not count(deny) > 0

# Decision the service reads. When any rule denies, expose the first reason;
# otherwise allowed with no reason.
default result := {"allowed": true, "reason": ""}

result := {"allowed": false, "reason": reason} if {
    reason := deny[_]
}