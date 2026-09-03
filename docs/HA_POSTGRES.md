# Multi-replica HA with Postgres

This document walks an operator through running two or more
`rousseau-agent` replicas behind a shared Postgres so a single
pod restart, node loss, or rolling deployment never drops a
conversation mid-turn.

## When to reach for this

Single-replica `sqlite` (the default) is the right answer for
almost every deployment. Reach for Postgres HA only when one of
these applies:

- You have a contractual uptime SLA and cannot tolerate the
  ~30 s cold-restart window a `Recreate` rollout gives you on the
  single-pod topology.
- You need active-active read replicas so an on-call operator
  can query the session store from a second pod without pausing
  the primary.
- Your ops posture treats "sqlite on a PVC" as a compliance
  finding regardless of the actual failure model.

If none of these apply, stay on sqlite. It is faster to boot,
faster per turn (no network hop per write), and has zero
external-service dependency.

## What's actually shared

`state.driver = postgres` moves the load-bearing session store
to Postgres:

- **Shared across replicas** — session ID → payload,
  message history, session ↔ sender mapping (backs
  `/sessions`, `/find`, `/resume`, `/delete`), FTS indices
  (sqlite FTS5 becomes tsvector).
- **Per-replica (still sqlite-local)** — cron job schedule,
  WhatsApp JID map, OAuth token cache, session cost ledger,
  Claude session cache. Each replica keeps its own copy under
  `/var/lib/rousseau`. See `internal/state/postgres/store.go`
  for the scope note.

Concretely: if replica A dies mid-turn, replica B can pick up
the same conversation on the next inbound because the *session
state* is in Postgres. But cron jobs scheduled on A won't fire
until A comes back, and a WhatsApp pairing done on A won't
transfer to B (WhatsApp is per-pod by protocol — this is a
transport limit, not a rousseau one).

## Prerequisites

- Postgres 14 or newer. The FTS surface uses `websearch_to_tsquery`
  and generated columns, both stable in 12+ but tested against 14+.
- A DSN with `sslmode=require` (or `verify-full` for stricter
  posture). Never use `sslmode=disable` in production.
- A dedicated database, e.g. `rousseau`. The schema is applied
  automatically on first `Open`.
- Kubernetes 1.24+ if you're using the shipped Helm chart.

## Step 1: Provision Postgres

Any Postgres works — RDS, Cloud SQL, Neon, self-hosted. The
schema is applied by the daemon at boot via
`internal/state/postgres/schema.sql` (embedded in the binary),
so you do NOT run migrations by hand.

Create the database + role:

```sql
CREATE ROLE rousseau LOGIN PASSWORD '<strong-password>';
CREATE DATABASE rousseau OWNER rousseau;
GRANT ALL PRIVILEGES ON DATABASE rousseau TO rousseau;
```

For AWS RDS multi-AZ or a managed HA offering, the failover
path is transparent to the daemon — the pgx driver reconnects
on the next query and Postgres holds the row-level lock so
concurrent writers to the same session serialise on the primary.

## Step 2: Configure the daemon

Point `state.driver` at `postgres` and set `state.dsn`.

`config.yaml`:

```yaml
state:
  driver: postgres
  dsn: postgres://rousseau:<password>@postgres-primary:5432/rousseau?sslmode=require
```

Or via env:

```
ROUSSEAU_STATE_DRIVER=postgres
ROUSSEAU_STATE_DSN=postgres://rousseau:...@postgres-primary:5432/rousseau?sslmode=require
```

The DSN is the only extra knob. Connection pooling uses pgx
stdlib defaults — sized for the daemon's actual concurrency
(~1 write per user message + occasional list) which sits far
below the pool's headroom on any production Postgres.

## Step 3: Scale replicas

In the Helm chart:

```yaml
replicaCount: 3

config:
  state:
    driver: postgres
    dsn: postgres://rousseau:...@postgres-primary.postgres.svc:5432/rousseau?sslmode=require

# Persistence is still needed for per-replica sqlite (cron,
# jidmap, etc.). Size accordingly.
persistence:
  enabled: true
  size: 2Gi
```

Or manually with `podman kube`:

```
podman play kube deploy/quadlet/rousseau-agent.yaml --replicas 3
```

Two things to know:

- **Transport pairing is per-replica for WhatsApp.** WhatsApp
  pairs a QR code to a session, and that session lives inside
  whichsmever pod scanned it. Multi-replica WhatsApp means
  each replica handles its own set of paired numbers. For
  cross-replica failover on WhatsApp specifically, run two
  pods paired to two numbers behind a routing layer.
- **Slack / Discord / Matrix / Signal / SMS / email transports
  are stateless.** They handle failover cleanly — any replica
  can service any inbound.

## Step 4: Verify

From a shell into one replica:

```
$ rousseau session list --limit 5
```

The same list should return from every replica. If replica B
shows fewer rows than replica A, the DSN is misconfigured — A
is still on its local sqlite, B is on Postgres, and they're
not sharing anything. Common cause: `ROUSSEAU_STATE_DRIVER`
env var set only on some pods, or `state.driver:` typoed in
one values file.

A stronger smoke test is to drive an inbound to one replica,
kill that pod, and send a follow-up. The follow-up should
land on a different replica and pick up context from the
same session:

```
$ kubectl exec rousseau-agent-0 -- rousseau chat "start a new session about deploys"
$ kubectl delete pod rousseau-agent-0     # forces reschedule
$ kubectl exec rousseau-agent-1 -- rousseau chat --sender +test "the same session?"
```

## Recovery scenarios

**Replica crash.** Kubernetes reschedules the pod. Sessions
resume on the next inbound — the new pod loads state from
Postgres. Recovery time is bounded by pod-start latency,
typically 10–30 s. No manual intervention.

**Postgres primary failover.** If you're on a managed HA
Postgres, the DSN transparently reconnects to the new primary
via DNS. The pgx driver retries the next query. Any in-flight
Save at failover time will error once — the daemon returns
the LLM reply but logs the persistence failure. This is the
one narrow window where a message is spoken but not recorded.
To minimise it, use a managed Postgres with failover under 30 s.

**Split-brain.** Cannot happen at the session level. Postgres
row-level locking serialises concurrent updates to the same
session ID. Two replicas writing the same session ID interleave
their `INSERT ... ON CONFLICT DO UPDATE`; the last writer wins.
Since sessions are per-sender and a single sender routes to a
single replica (per transport), two replicas rarely write the
same session anyway.

## What NOT to do

- **Don't mix drivers.** A cluster of N replicas MUST all be
  on the same driver. sqlite-A and postgres-B do not share
  anything and you will silently split state.
- **Don't point two independent clusters at the same
  Postgres.** The daemon assumes it owns the schema — two
  clusters both running the auto-migration on boot is fine
  because everything is idempotent, but two clusters both
  writing sessions makes debugging "why do I see other
  people's data" much harder than the value it saves.
- **Don't disable persistence when only some transports need
  it.** The daemon still writes per-replica sqlite for cron
  and jidmap. `persistence.enabled: false` breaks cron
  schedule survival across restarts even on Postgres HA.

## Monitoring

Every replica exposes `/metrics` on port 9090. The postgres-
specific metrics to watch:

- `rousseau_state_save_duration_seconds` — sudden spikes
  indicate lock contention or a slow primary.
- `rousseau_state_load_errors_total` — non-zero means the
  pool is exhausted or the primary is unreachable.
- Postgres itself: `pg_stat_activity` for connection count,
  `pg_stat_statements` for slow queries.

For the daemon's own liveness: `/healthz` on the same port
returns 200 iff the primary is reachable. Wire this into your
readiness probe so Kubernetes routes traffic away from a
replica that has lost Postgres.

## Cost / capacity

The write path per user turn is small — one `sessions` upsert
(payload as JSON) plus a few audit rows. A `db.t4g.small` on
RDS comfortably handles 100+ concurrent daemons. Storage is
proportional to session count × average payload; a session
with 100 messages is ~30 KB, so 100k sessions = 3 GB.

## Limitations shipped today

- Cron schedules, WhatsApp JID pairings, and session cost
  ledgers stay per-replica. Roadmap §2.4b covers the port.
- No pgx `pgxpool` tuning surface yet — the stdlib bridge
  uses defaults that suit the daemon's concurrency but
  operators wanting `MaxConns` control need to wait for the
  planned `state.pool` config block.
- Read replicas are supported by the driver (any DSN reaches
  a primary or a replica), but the daemon assumes writeable
  connections. Point the DSN at the primary; use a separate
  DSN for out-of-band analytics against a replica.

## When to reach out

If a rolling deployment drops a WhatsApp pairing or you see
`state.load_errors_total` climbing under load, open an issue
with the `state/postgres` label. Include:

- Postgres version and hosting environment.
- Daemon version (`rousseau version`).
- `pg_stat_activity` snapshot at the time of the incident.
- Whether you're multi-region (a lot of "slow Save" reports
  end up being cross-region latency, not a daemon issue).
