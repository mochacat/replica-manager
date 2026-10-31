# Kubernetes Replica Manager

**Author:** Cat Moca

## What

A gRPC service that manages Kubernetes Deployment replica counts via a controller with reconciliation, leveraging K8s watch caching and mTLS for secure communication.

## Why

A reliable way to manage replica counts across Kubernetes Deployments. This service provides a secure, cached, and reconcilable interface for replica management — a control plane for Deployment scaling with mTLS and native state management.

We choose gRPC over HTTP because it's a more natural fit for the Kubernetes API server.

# Scope

We'll focus on the gRPC API and the controller reconciliation loop using controller-runtime. Desired replica counts are stored as annotations on the Deployment itself, so the Deployment is the single source of truth and the reconciler is triggered naturally by annotation changes. A CRD-based approach is deferred to a future iteration.

* gRPC API to retrieve the replica count of the Kubernetes Deployment
* gRPC API to set the replica count of the Kubernetes Deployment
* gRPC API to get the list of available Deployments in the Kubernetes cluster
* gRPC API must cache the replica count by watching for changes to Deployments.
  Read-only requests should not each trigger a request to the cluster.
* gRPC or HTTP health check verifying Kubernetes connectivity
* Secure connections between the gRPC API and caller with mTLS
* One or two tests that cover happy and unhappy scenarios
* Ability to build and deploy all artifacts to a Kubernetes cluster using make
* Ability to execute integration tests against the local Kubernetes cluster
  using make
* Create a configurable Helm chart for the service
* Include production-level packaging for this service, including but not
  limited to: Deployment, Role, RoleBinding, ServiceAccount, and Service
* Upgrading the Helm chart should not result in unavailability of the gRPC API
  portion of the service

## Details

### High-level design of API and controller

Reads API
```
GetReplicaCount RPC / ListReplicas RPC → controller-runtime client.Get/List
                                           ↓
                                      reads from manager's internal informer cache
                                      (no API server hit)
                                      ↓
                                 responds with replica data
```

Local cache (controller-runtime Manager)
```
Manager starts
  → built-in informer issues List to fetch current Deployments
  → informer opens Watch for change events
  → informer populates internal cache on each ADDED / UPDATED / DELETED event
  → informer reconnects and re-lists on watch disconnect
```

Desired state (Deployment annotation)
```
The desired replica count is stored as an annotation on the Deployment itself:

    metadata:
      annotations:
        replicamanager/replicas: "5"

The handler patches only the annotation (not spec.replicas). This triggers the
informer → reconciler. The reconciler reads the annotation, compares against
spec.replicas, and patches if different.

This is a lightweight CRD — the annotation acts as the desired-state resource,
the Deployment spec is the current-state resource, and the reconciler is the
control loop that converges them. The pattern is identical to a CRD controller:
watch a desired-state object (annotation), compare to current state (spec),
reconcile differences.

The Deployment remains the single source of truth, the desired count survives
restarts, and anyone can read it with kubectl. A proper CRD (with its own API
version, validation, and RBAC) can replace this in a future iteration.
```

Writes API
```
SetReplicas RPC → patches Deployment annotation to desired count
                   ↓
              informer detects annotation change → reconciler fires
                   ↓
              reconciler reads annotation, compares spec.replicas
                   ↓
              if different → patches spec.replicas (eventual consistency)
```

### API Design (gRPC)

GET /replicas/{namespace}/{name}
LIST /replicas/{namespace}

SET /replicas/{namespace}/{name}


```proto
syntax = "proto3";

import "google/protobuf/empty.proto";

package replicamanager;

service ReplicaService {
  rpc GetReplicaCount(GetReplicaCountRequest) returns (GetReplicaCountResponse) {}
  rpc ListReplicas(ListReplicasCountRequest) returns (ListReplicasResponse) {}
  rpc SetReplicas(SetReplicasCountRequest) returns (google.protobuf.Empty) {}
  rpc HealthCheck(google.protobuf.Empty) returns (HealthCheckResponse) {}
}

message Replica {
  string name = 1;
  int32 desired = 3; // spec.replicas
  int32 ready = 4; //status.readyReplicas
  int32 available = 6; // status.availableReplicas
}

message GetReplicaCountRequest {
  string namespace = 1;
  string name = 2;
}

message GetReplicaCountResponse {
  string namespace = 1;
  string name = 2;
  Replica replica = 3;
}

message ListReplicasRequest {
  string namespace = 1;
}

message ListReplicasResponse {
  string namespace = 1;
  repeated Replica replicas = 2;
}

message SetReplicasCountRequest {
  string namespace = 1;
  string name = 2;
  int32 desired = 3; // spec.replicas (ie desired)
}

message HealthCheckResponse {
  bool healthy = 1;
}
```

<!-- Proto file definition — RPCs for GetReplicaCount, SetReplicaCount, ListDeployments. Unary RPCs vs server streaming for watch integration. Request/response message shapes. Error handling (not found, forbidden, etc.) -->

### Caching & Watch Integration

Get and list shouldn't hit the API server every time. The [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime) `Manager` provides a built-in informer-backed cache that handles this automatically.

When the Manager starts:
1. Its internal informers issue an initial List to fetch current Deployments.
2. The informers open a Watch for change events.
3. Each `ADDED` / `UPDATED` / `DELETED` event updates the cache automatically.
4. On watch disconnect, controller-runtime reconnects and re-lists transparently.

The gRPC handlers read through `mgr.GetClient()` — reads hit the local cache, not the API server. Writes (via `SetReplicas`) patch only the Deployment annotation; the resulting watch event updates the cache and triggers the reconciler.

**Desired state** is stored as a Deployment annotation:

```yaml
metadata:
  annotations:
    replicamanager/replicas: "5"
```

This replaces the earlier in-memory map. The Deployment is the single source of
truth — desired count survives restarts, and the annotation change naturally
triggers the reconciler via the informer. A CRD can replace this in a future
iteration.

Note: there will be a slight lag between a write and the cache reflecting it
(the watch event must propagate), so reads may return stale data briefly.

#### Reconciler trigger mechanism

The reconciler watches all Deployments via `For(&appsv1.Deployment{})`. When
`SetReplicas` patches the annotation, the informer detects the change and the
reconciler fires. No separate trigger mechanism is needed — the annotation is
part of the Deployment, so any annotation change is a K8s event.

#### Timing

```
SetReplicas("foo", "bar", 5)
  T0: handler.Patch(deployment annotation: "replicamanager/replicas"="5")
  T1: Informer detects annotation change → Reconciler fires
  T2: Reconciler reads annotation → 5
  T3: Reconciler reads spec.replicas → 3
  T4: 5 != 3 → patches spec.replicas to 5
  T5: Informer detects spec change → Reconciler fires
  T6: Reconciler reads annotation → 5
  T7: Reconciler reads spec.replicas → 5
  T8: 5 == 5 → converged, no-op
```

Two reconcile loops per SetReplicas is expected and correct — the first loop
acts on the annotation change, and the second verifies convergence after the
spec patch. controller-runtime serializes reconcile requests per key
(namespace/name), so concurrent reconciles for the same Deployment do not race.

**Annotation update failure case**: If the annotation patch fails (network
error), the handler returns an error to the caller and the Deployment is
unchanged. No informer event fires.

**External change case**: If `kubectl scale` changes spec.replicas without
changing the annotation, the informer fires and the reconciler corrects
spec.replicas back to the annotation value. If there is no annotation, the
reconciler is a no-op — external scaling is allowed for unmanaged Deployments.

**Annotation tampering**: If someone directly edits the annotation via
`kubectl annotate`, the reconciler treats it as a desired state change and
patches spec.replicas to match. This is by design — the annotation is the
source of truth, regardless of who set it.

<!-- Level 4+ requirement: cache replica counts by watching Deployments for changes using client-go or controller-runtime. Read requests hit the cache, not the cluster. Invalidation strategy on write. -->

### Controller & Reconciliation

We use [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime) to implement the reconciliation loop. The Manager runs a Reconciler that watches Deployments and ensures each one matches its desired replica count.

**Desired state** is stored as a Deployment annotation (`replicamanager/replicas`). The `SetReplicas` gRPC handler patches only the annotation — it never writes `spec.replicas` directly. The Reconciler reads the annotation, compares against `spec.replicas`, and patches if they differ. A CRD-based approach is deferred to a future iteration.

A Deployment without the annotation is treated as unmanaged — the reconciler is a no-op. This allows `kubectl scale` to work on Deployments that were never managed by our API.

On restart, the controller-runtime cache populates from live cluster state via the initial List, and the reconciler reconciles every Deployment (checking for the annotation on each).

#### Pseudocode
```go
const AnnotationKey = "replicamanager/replicas"

// desiredFromAnnotation parses the desired replica count from a Deployment's
// annotation. Returns 0, false if the annotation is missing or unparseable.
func desiredFromAnnotation(dep *appsv1.Deployment) (int32, bool) {
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

// Reconciler watches Deployments and reconciles to desired state.
type Reconciler struct {
	client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	patch := client.MergeFrom(dep.DeepCopy())
	dep.Spec.Replicas = &desired
	return ctrl.Result{}, r.Patch(ctx, &dep, &patch)
}


// main wires the Manager, Reconciler, and gRPC server together.
func main() {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		log.Fatal(err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Complete(&Reconciler{
			Client: mgr.GetClient(),
		}); err != nil {
		log.Fatal(err)
	}

	go func() {
		internal.Start(ctx, mgr.GetClient(), &ready)
	}()

	mgr.Start(ctx)
}
```

### mTLS Configuration

The grpc client (or caller) will need to auth with the server. We will use mTLS for authentication. 

For this POC we will generate a self-signed CA, issue the server cert by that CA, issue the client cert by the CA, and have both sides verify against the CA.
We will bundle that CA cert into a helm chart.

Choosing ECDSA over RSA for PoC (P-384 keys, TLS 1.3).

As for key management, it is pre-generated in-repo for PoC for now.

### Developer Workflow

Prerequisites: Go 1.26+, make, Docker, KIND, helm, grpcurl (or equivalent)

```bash
make build # compile Go binary and build Docker image
make kind-up # spin up KIND cluster, no-up if up already
make deploy # helm upgrade --install replica-manager helm/ --values helm/values.yaml --namespace default
make integration-test # deploy to KIND, create Deployment, verify get/set, test caching, test reconciliation
make clean # delete deployment, tear down KIND
```

example curl commands:
```bash
grpcurl --cacert ./certs/ca.pem --cert ./certs/client.pem \
  --key ./certs/client-key.pem localhost:6443 \
  ReplicaService/ListReplicas
```

### Build & Release

#### Docker

Multi-stage Go build with minimal distroless base image: 
```
ARG BASE_IMAGE=gcr.io/distroless/base-debian12

# Builder stage — bookworm is Debian 12, same base as distroless/base-debian12
FROM golang:1.26-bookworm as builder
WORKDIR /src

# Cache dependency download
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .

# Generate mTLS certs and proto stubs before building
RUN make certs proto && go build -o /app/service .

# Runtime stage
FROM ${BASE_IMAGE}
COPY --from=builder /app/service /service
COPY --from=builder /etc/ssl/certs /etc/ssl/certs

ENTRYPOINT ["/service"]
```

#### Helm Chart

Charts:
```
helm/
  Chart.yaml
  values.yaml
  templates/
    deployment.yaml
    service.yaml
    service-account.yaml
    role.yaml
    role-binding.yaml
    network-policy.yaml # optional
```


```yaml
apiVersion: v1
kind: Service
metadata:
  name: replica-manager
spec:
  selector:
    app: replica-manager
  ports:
    - port: 6443
      targetPort: 6443
```

deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: replica-manager
spec:
  selector:
    matchLabels:
      app: replica-manager
  replicas: 1
  template:
    metadata:
      labels:
        app: replica-manager
    spec:
      containers:
        - name: replica-manager
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          ports:
            - containerPort: 6443
          env:
            - name: SERVER_CERT
              value: /certs/server.crt
            - name: SERVER_KEY
              value: /certs/server.key
            - name: CA_CERT
              value: /etc/ssl/certs/ca-certificates.crt
```


#### Automation

Make targets: build, test, integration-test, deploy, clean.

* build: builds the binary and docker image
* test: runs the unit tests
* integration-test: runs the integration tests
* deploy: deploys the server to the cluster (ie `helm upgrade --install replica-manager helm/ \
                --values helm/values.yaml --namespace default`)
* clean: deletes the server from the cluster

### Security Considerations

<!-- mTLS as the sole auth mechanism. ServiceAccount permissions scoped to read/write Deployments and manage CRD. Secrets (CA key, server cert) — pre-generated for PoC. -->

mTLS is the only auth mechanism we're using. We're using a self-signed CA for PoC. This will be in the repo for the POC but we really should use something like cert-manager or a secret store. 

We are using Role and RoleBinding to manage permissions on the server. We're using a Role that allows read/write access to Deployments, and a RoleBinding that grants the server the read/write permissions on the Deployments resource.

We should also update the NetworkPolicy to allow the server to talk to the API server, and the API server to talk to the server.

#### Optional replica policy (embedded OPA, no CRD)

An additional, **opt-in** security layer that constrains the replica counts the
controller will actually apply. It is designed for environments where writers
are partially trusted or fully autonomous (infra agents, automation, service
accounts) and a bad actor — or a typo — could otherwise push a Deployment to a
ridiculous replica count.

Security model. The policy is **baked into the container image** and read at
startup. Nothing in the cluster can modify it — there is no writable policy
object to tamper with, which is the point. This is what makes the layer
tamper-resistant against insiders who already have `patch deploymments`
permission: the bounds the reconciler trusts are *not* stored in the cluster.

Design rules (confirmed):

* **Lightweight**: no CRD, no admission webhook, no second container. Policy
  hosts *inside* the existing process via `github.com/open-policy-agent/opa/rego`
  embedded as a library. One `.rego` file, compiled once at startup, reused for
  every evaluation.
* **Opt-in by default**: disabled unless `REPLICA_POLICY_ENABLED=true` is set.
  When disabled, behavior is byte-for-byte today's. When enabled, a policy that
  fails to compile is a **fatal startup error** — we never run a "secure" mode
  silently without policy.
* **Single global policy, cross-namespace via rules**: one policy applies to
  every namespace the controller manages; per-namespace bounds are expressed as
  *rules inside that one immutable file* (e.g. `allow namespace == "prod" and
  input.desired <= 10`), not as separate mutable per-namespace objects.
* **Enforced at two chokepoints from one implementation** (single source of
  truth via a shared `internal/policy` package):
  * gRPC handler `SetReplicas` — rejects an out-of-policy `desired` with a clear
    gRPC error, synchronously, so legitimate-but-mistaken callers hear about it
    immediately.
  * Reconciler — re-checks the annotation value against the *same* policy before
    patching `spec.replicas`. This is the tamper-proof backstop: even if an
    insider edits the `replicamanager/replicas` annotation directly (bypassing
    the handler), the reconciler refuses to apply an out-of-policy count.
* **Above max → strict refuse, never clamp**. This guards the typo-overflow
  case (an operator means `10`, types `1000`). Clamping to a soft max would
  still materialize `max` pods the operator never wanted; refusing scales
  nothing and surfaces a loud error instead. Applies in both handler and
  reconciler.
* **Below min → refuse in the handler; reconciler refuses but does NOT force
  replicas back up.** Auto-rising replicas to satisfy min can fight a
  legitimate emergency scale-down, so the reconciler never force-raises; it
  logs and leaves `spec.replicas` untouched.
* **Violations are loud**: structured `ERROR`/`WARN` logs at both layers
  identifying the deployment, requested vs. allowed, and the offending rule.

Policy evaluation shape. Rego receives an `input` document such as
`{"namespace","name","desired","current","min","max"}` and returns an `allow`
decision. Bounds (`min`/`max`) come from the policy itself (constants or
per-namespace rules) — the caller cannot supply them.

Baking into the image. The policy file is embedded at build time via
`go:embed` and/or `COPY` into the Docker image at `/app/policy`. No runtime
mount; the bytes are fixed by the image. Changing policy requires a rebuild and
redeploy, which is the deliberate tradeoff for tamper-resistance.

### Observability

<!-- Health check gRPC or HTTP endpoint. Structured logging. Metrics (replica count, reconciliation runs, cache hits/misses). -->


#### Health check
Health check is a separate gRPC endpoint that returns a boolean for healthy when the service has a live k8s connection and the controller is running. It will return false if the connection is lost or controller failure.

#### Structured logging

standard JSON logging via stdout via Go's slog package.
* `INFO` level for normal operation of reconciliation loop and API server, cache hits/misses
* `WARN` level for recoverable errors like cache invalidation, etc
* `ERROR` level for unrecoverable errors like controller failing to start, reconciliation loop failing, etc.

#### Metrics

For future, Prometheus metrics for replica count and reconciliation runs, cache hits/misses.

## Test Plan

## Unit tests

We will have simple grpc unit tests to test the endpoints. We can just have one or two tests for the get/set and list endpoints.

## Integration tests

We can deploy to KIND, create a deployment, verify the get/set, test caching, test reconciliation. We can spin up a quick docker container with KIND and run the tests in there.

### Smoke tests

For now we will deploy to a pre-prod cluster and verify end-to-end

## Open Questions

1. For caching, when the set replicas is written do we invalidate immediately or wait for the watch event? 
2. The reconciliation loop works for one replica, but if we had multiple we would have multiple reconciliation loops running? Is that going to work?
3. If there is a desired count update requested for a non-existent deployment, should we return an error or just ignore it?