# Kubernetes Replica Manager

A secure gRPC control plane for declarative Kubernetes scaling — manages
Deployment replica counts via annotation-based desired state with a
controller-runtime reconciler.

Built for programmatic, event-driven, and agent-controlled infrastructure where
direct `kubectl` access is too broad but HPA is too narrow.

Think of it as a **lightweight CRD**: the `replicamanager/replicas` annotation is
your desired-state resource, the Deployment `spec.replicas` is your current state,
and the reconciler is the control loop that converges them — all without deploying
a single CRD.

## Who this is for

- **Platform engineers** building internal scaling APIs
- **Infra automation authors** who need a safe, authenticated scaling endpoint
- **AI/agent infrastructure developers** looking for a bounded tool-use surface
- **K8s controller developers** wanting a clean reference implementation of the
  controller-runtime reconciler pattern with gRPC + mTLS

## Why not just use kubectl scale / HPA / KEDA?

- **`kubectl scale`** requires direct API access and scatters scaling logic across
  scripts and CI pipelines
- **HPA** is metric-driven — great for CPU/memory, awkward for scheduled or
  event-driven scaling
- **KEDA** is powerful but heavy — you may not need the full event-driven
  autoscaling surface

Replica Manager fills the gap: a narrow, authenticated gRPC API that anything can
call — a cron job, a webhook handler, a CI pipeline, or an AI agent — to scale
Deployments programmatically. The mTLS boundary and bounded mutation surface make
it safe to expose to automation that shouldn't have full `kubectl` access.

### Use cases

- **Scheduled scaling** — scale down staging at 6pm, back up at 8am
- **Event-driven teardown** — CI finishes → scale down preview environments
- **Canary rollouts** — shift replica ratios between stable and canary deployments
- **Agent-controlled infrastructure** — give an LLM a typed, bounded API instead
  of raw `kubectl`

## Quickstart

```bash
make kind-up            # create a KIND cluster
make build              # build binary + Docker image
make deploy             # load image into KIND, Helm install

# If you have multiple K8s contexts, switch to the KIND one first:
kubectl config use kind-replica-manager
```

## Test the server

```bash
grpcurl --cacert certs/ca.pem --cert certs/client-public.pem \
  --key certs/client-private.pem \
  -import-path api/replicamanager/v1 \
  -proto api/replicamanager/v1/service.proto \
  localhost:8443 \
  replicamanager.ReplicaService/HealthCheck
```

Expected output:
```json
{
  "healthy": true
}
```

## Prerequisites

- Go 1.26+
- make
- protoc (via mise: `mise install protoc`)
- Docker, KIND, helm (for deployment)
- grpcurl (for testing)

If you have multiple K8s clusters (e.g. k3d, minikube), set the active
context to the KIND cluster before deploying:

```bash
kubectl config use kind-replica-manager
```

## Project structure

```
├── Dockerfile                  # Multi-stage distroless build
├── cmd/server/main.go          # Entry point (Manager + gRPC)
├── internal/
│   ├── server.go               # gRPC server setup, mTLS
│   ├── handler.go              # RPC handlers (reads via controller-runtime client)
│   └── handler_test.go         # Handler tests
├── controller/
│   ├── reconciler.go           # Controller-runtime Reconciler
│   └── reconciler_test.go      # Reconciler tests
├── api/replicamanager/v1/      # Protobuf definitions
├── certs/                      # TLS certificates (generated)
├── scripts/                    # Build/deploy scripts
├── helm/                       # Helm chart
└── test/                       # Integration tests
```

## Integration tests

The integration test runs as a pod inside the KIND cluster, connecting to the
service via in-cluster DNS. It validates the full end-to-end flow:

- gRPC HealthCheck
- List/Set/Get replica counts
- Scaling to 0
- Controller reconciliation (verifies `spec.replicas` via the K8s API)
- mTLS rejection for unauthenticated clients

```bash
make integration-test    # full cycle: deploy + test + cleanup
```

## Certificate management (Bring Your Own Certificates)

The gRPC server authenticates callers with **mutual TLS (mTLS)** — both the
server and the client present a certificate, and each verifies the other against
a shared CA.

Replica Manager is **certificate-agnostic**: it does not care who issues your
certificates. It only expects a Kubernetes Secret holding that CA and the server
keypair, mounted at `/app/certs`. Whatever tooling your organization already
uses for certs — cert-manager, HashiCorp Vault, cloud KMS, an internal CA, or
plain `kubectl create secret` — will work. The chart ships with no key material
bundled by default.

### Required Secret layout

The chart expects a Secret (default name `replica-manager-certs`) with these keys:

| Key | Purpose |
|-----|---------|
| `ca.pem` | CA certificate — used by the server to verify clients |
| `server-public.pem` | Server certificate |
| `server-private.pem` | Server private key |

The server only needs these three. Create the Secret however you like, e.g.:

```bash
kubectl create secret generic replica-manager-certs \
  --from-file=ca.pem=./ca.pem \
  --from-file=server-public.pem=./server-public.pem \
  --from-file=server-private.pem=./server-private.pem
```

Point the chart at your Secret:

```yaml
certs:
  create: false          # (default) do not bundle dev certs
  secretName: my-certs   # reference an existing Secret
```

### The mTLS trust model

A few points your certificate setup must respect:

- **The server's own certificate** must be issued by the CA and include a SAN
  matching how clients reach it (in-cluster DNS, e.g.
  `replica-manager.my-ns.svc.cluster.local`, or an ingress host).
- **The CA key should not live on the server pod.** It signs client/server
  certs and belongs inside your issuer or secret store, not the running service.
- **Client certificates** are validated against the same CA. Issue them to each
  caller (a CLI, an integration test, an agent). Nothing else is required.
- Certificates should be **rotated** on a schedule and **revoked** on compromise,
  per your organization's policy. Replica Manager re-reads the Secret on pod
  restart, so rotating normally means updating the Secret and restarting the pod
  (or letting cert-manager reconcile it).

### Option A — Bring your own Secret (production)

This is the production path. Create the Secret with your own certs (see above)
and optionally gate on `certs.create: false` (the default). Nothing else
changes.

### Option B — cert-manager (recommended for automation)

Use cert-manager to issue and auto-rotate the server certificate against your
internal CA. Disable the chart's own Secret (`certs.create: false`) and add a
`Certificate` resource that cert-manager fills into a Secret:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: replica-manager
  namespace: replica-manager
spec:
  secretName: replica-manager-certs   # must match certs.secretName
  issuerRef:
    name: my-internal-ca
    kind: ClusterIssuer
  dnsNames:
    - replica-manager.replica-manager.svc.cluster.local
```

### Option C — Dev certificates (local KIND / demos)

For local development only, generate throwaway certs and let the chart render
them into a Secret:

```bash
make certs      # generate CA + server + client certs
```

```yaml
certs:
  create: true   # OPT-IN: render a Secret from these dev certs
```

**Server SAN:** the generated server certificate includes DNS names for
`localhost` and the in-cluster service DNS. To customize (e.g. a different
namespace or ingress), set the `CERT_SAN` variable:

```bash
CERT_SAN="DNS:localhost,DNS:my-custom-ns.svc.cluster.local" make certs
```

> These dev certs are generated on your workstation and bundled into the chart
> for convenience. They are not suitable for production — they do not rotate and
> the CA key sits on disk. Use Option A or B in real environments.

## Make targets

| Target | Description |
|--------|-------------|
| `make certs` | Generate CA, server, and client certs |
| `make proto` | Compile proto to Go stubs |
| `make build` | Build the Go binary and Docker image |
| `make test` | Run unit tests |
| `make kind-up` | Create a KIND cluster |
| `make deploy` | Deploy Helm chart to cluster |
| `make integration-test` | Run integration tests against KIND (creates, tests, cleans up) |
| `make integration-test-ci` | Run integration tests in CI (cluster managed externally) |
| `make clean` | Tear down cluster and remove binaries |

## Architecture

### Lightweight CRD pattern

The desired replica count is stored as a Deployment annotation rather than a
separate Custom Resource Definition. This avoids CRD deployment ordering, schema
versioning, and API server bloat — the Deployment itself is the single source of
truth:

```
  SetReplicas gRPC
       │
       ▼
┌──────────────────────┐
│  Annotation (desired) │ ──→  Reconciler  ──→  spec.replicas (current)
│ replicamanager/      │        │                       │
│   replicas: "5"      │   converges diff              │
└──────────────────────┘        │                       │
       │                        ◄──── Informer watch ────┘
       │
       ▼
  kubectl get deployment
  (anyone can read it)
```

This is the same pattern used by production controllers — annotation-as-desired-state
is well-established in the ecosystem (cert-manager, Istio, and many operators use it).
A proper CRD can replace the annotation in a future iteration without changing the
reconciler logic.

The service uses a controller-runtime reconciler that watches all Deployments
and synchronizes `spec.replicas` to match the `replicamanager/replicas` annotation.
Clients never write `spec.replicas` directly — they set the annotation via the gRPC
API, and the reconciler applies it.

```
Client → gRPC SetReplicas → annotation → controller → spec.replicas → K8s
```

### How it works

1. **Reads** (`GetReplicaCount`, `ListReplicas`) hit the controller-runtime
   informer cache — zero API server calls for read-only operations
2. **Writes** (`SetReplicas`) patch only the Deployment annotation, which triggers
   the informer and fires the reconciler
3. **Reconciler** compares the annotation against `spec.replicas` and patches if
   they differ — eventual consistency with convergence verification
4. **mTLS** with P-384 ECDSA certs, TLS 1.3, and `RequireAndVerifyClientCert`
   — no unauthenticated access