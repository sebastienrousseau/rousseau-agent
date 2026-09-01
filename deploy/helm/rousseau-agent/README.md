# rousseau-agent Helm chart

Kubernetes deploy target for `rousseau-agent` — the same self-hosted daemon shipped as `podman run` and systemd Quadlet, packaged for a cluster.

Delivers the ROADMAP §2.4 pledge to make "can I deploy this in my cluster?" a one-command answer. First question every enterprise trial asks; a hard-to-deploy OSS product has no paid conversion funnel.

## Install

```bash
helm install rousseau ./deploy/helm/rousseau-agent \
  --namespace rousseau-agent --create-namespace
```

The default install produces a single-replica sqlite-backed daemon running the WhatsApp bridge — behaviourally identical to `podman run ghcr.io/sebastienrousseau/rousseau-agent:latest whatsapp`.

## Common overrides

**Different transport:**

```bash
helm install rousseau ./deploy/helm/rousseau-agent \
  --set command=slack \
  --set-string envFromSecret=slack-tokens
```

**Enable the Prometheus ServiceMonitor** (requires Prometheus Operator CRDs):

```bash
helm install rousseau ./deploy/helm/rousseau-agent \
  --set serviceMonitor.enabled=true
```

**Attach an enterprise licence:**

```bash
helm install rousseau ./deploy/helm/rousseau-agent \
  --set-file licence.value=/path/to/licence.key
```

Or reference a pre-created Secret (recommended for GitOps flows):

```yaml
# secrets.yaml — apply separately (or via ExternalSecret)
apiVersion: v1
kind: Secret
metadata: {name: rousseau-secrets}
stringData:
  ROUSSEAU_LICENSE_KEY: "eyJz...bg"
  ANTHROPIC_API_KEY: "sk-ant-…"
```

```bash
helm install rousseau ./deploy/helm/rousseau-agent \
  --set envFromSecret=rousseau-secrets
```

## Multi-replica HA

`replicaCount > 1` REQUIRES `state.driver=postgres`. Without it, every pod runs its own SQLite and session state does NOT cross pods — users see random gaps depending on which pod they land on.

The chart intentionally does NOT enforce this at render time (an operator may run the second replica as a hot standby of a shared state store the chart doesn't know about). It DOES warn in `helm install` NOTES.

Minimal HA values.yaml:

```yaml
replicaCount: 3
config:
  state:
    driver: postgres
    dsn: postgres://user:pass@rousseau-agent-postgresql:5432/rousseau?sslmode=require
```

The Postgres subchart is deliberately not a hard dependency — most enterprises want to point at their own managed Postgres, not spin up a per-app one. Bring the DSN.

## Values reference

Every knob has a comment in [`values.yaml`](values.yaml). Highlights:

| Key | Default | Purpose |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/…/rousseau-agent` / chart's `appVersion` | Which daemon image to run |
| `replicaCount` | `1` | Multi-replica requires `driver=postgres` |
| `command` | `whatsapp` | The `rousseau <subcommand>` to run |
| `config` | `{}` | Full `config.yaml` inlined into a ConfigMap |
| `licence.value` | `""` | Inline enterprise licence → Secret + env |
| `licence.path` | `""` | File-based licence path (mounted separately) |
| `envFromSecret` | `""` | Reference a pre-created Secret for env vars |
| `persistence.enabled` | `true` | Create a PVC for `/var/lib/rousseau` |
| `serviceMonitor.enabled` | `false` | Emit a Prometheus Operator ServiceMonitor |
| `podSecurityContext` / `securityContext` | non-root, seccomp RuntimeDefault | Mirrors the Quadlet-hardening baseline |
| `strategy` | `Recreate` | Default — safer for long-lived transport sockets |

## Sanity check locally

Without a cluster:

```bash
helm lint deploy/helm/rousseau-agent
helm template test deploy/helm/rousseau-agent --set licence.value=x | kubeval  # or kubeconform
```

## Not in this chart

- **Postgres subchart** — bring your own DSN. Managed Postgres is a per-cluster choice.
- **Ingress** — the daemon speaks outbound to LLM providers + transports; nothing to expose. Prometheus scrapes come in via the Service.
- **HPA** — the workload is bounded by transport rate, not CPU. Vertical scaling is what the resources block is for.
- **NetworkPolicy** — cluster-specific; drop via `extraManifests`.
