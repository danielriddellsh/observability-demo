# Observability Demo — gold standard

A live walkthrough of what **mature, production-grade observability** looks like, built on Go + OpenTelemetry + the Grafana LGTM+P stack.

The point isn't just "we shipped OTel." It's the full practice: SLOs, error budgets, burn-rate alerts, runbooks, exemplars, trace ↔ log ↔ profile correlation, service maps, and deploy-correlation. Everything you'd expect a team that *actually runs production well* to have wired up.

> The demo runs locally with one command. A six-scene narrative takes about five minutes and tells the story of an SLO-breaching deploy, the alert that fires, the trace that pinpoints the root cause, the log that confirms it, the profile that shows the offending function, and the rollback that resolves it.

---

## Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) running
- [just](https://github.com/casey/just#installation) — `brew install just` on Mac
- A browser

---

## Quick start

```sh
git clone https://github.com/your-org/observability-demo
cd observability-demo
just up        # build, start, wait for Grafana
just open      # opens the SLO dashboard — the demo's home page
```

Traffic starts flowing immediately. Within ~2 minutes the SLO panels populate with realistic numbers.

---

## What's running

```
loadgen  ──►  api  ──►  worker  ──►  (simulated Redis cache, Postgres DB)
                                            │
                                            └─►  Pyroscope (profiles)
all three  ──►  otel-lgtm  (OTel collector + Grafana + Loki + Tempo + Prometheus + Pyroscope)
```

| Service | Role |
|---|---|
| **loadgen** | Variable-rate traffic generator with sine-wave + bursts |
| **api** | HTTP gateway, propagates trace context to worker |
| **worker** | Downstream service — talks to a simulated cache + DB with load-correlated behaviour |
| **otel-lgtm** | All Grafana telemetry backends in one container |

Every `/order` request produces an **8-span distributed trace** spanning two services, with cache hit/miss variation, queue-depth-correlated DB latency, and a clear `db.lookup` outlier when things degrade.

---

## What makes this "gold standard"

These are the practices that distinguish a mature observability setup from "we have dashboards":

| Practice | Where to see it |
|---|---|
| **SLOs with error budgets** | SLO dashboard — availability + latency, target 99% over 30d, budget remaining as a big number |
| **Multi-window burn-rate alerts** | Two SLOs × (fast burn 14.4×/1h + slow burn 6×/6h), Google SRE workbook style |
| **Runbook links on every alert** | Click any firing alert → "Runbook" link in payload → markdown playbook |
| **Exemplar-linked metrics** | Latency heatmap shows trace-ID dots; click any dot → the trace |
| **Trace ↔ log ↔ profile correlation** | From a span: "Logs for this span" → Loki, "Profile for this span" → Pyroscope flamegraph |
| **Service map** | Live topology from Tempo span metrics, animated by request volume |
| **Continuous profiling** | Worker exports CPU + memory profiles to Pyroscope |
| **Deploy markers** | `just deploy` puts a vertical line on every dashboard — change-correlation in one click |
| **Recording rules** | SLIs and burn rates pre-computed; dashboards query the aggregates not the raw histograms |
| **Resource-attribute slicing** | Every signal carries `service.version`, `deployment.environment`, `cloud.region`, `k8s.cluster.name` |
| **Structured logs with trace_id** | Every log line has `trace_id` and `span_id`; click → opens the trace |

---

## The demo — six scenes in five minutes

Run these in order. Each scene takes ~30–60 seconds.

### Scene 1 · The SLO dashboard is the home page

`just open` puts you on **[SLOs — Service Level Objectives](http://localhost:3000/d/slo)**.

What to point at:
- **Availability SLI (30d)** — currently ~99.9%, well above the 99% target.
- **Error Budget Remaining** — green, ~80%.
- **Burn Rate (1h / 6h)** — green, well below 1×.

Talking point: *"This is the page our on-call sees first. Not 'how many requests per second' — 'are we keeping the promises we made to customers?'"*

### Scene 2 · Break it — simulate a bad deploy

In another terminal:

```sh
just break
```

This places a deploy annotation (`worker v1.4.3 (regression)`) and toggles a chaos flag in worker that injects extra latency, errors, and a hot loop in the `compute()` function.

Watch the SLO dashboard. Within ~60 seconds:
- Availability SLI dips below 99%
- Error budget bar turns orange, then red
- Burn rate (5m / 1h) climbs past 14×
- A **Fast budget burn** alert fires (Alerting → Alert rules)

Talking point: *"Real production teams don't watch dashboards — alerts watch them. This burn rate alert would page someone within two minutes."*

### Scene 3 · Click the alert → land on Overview

Open Alerting → Alert rules → the firing alert → the linked **Runbook URL** opens the markdown playbook in `docs/runbooks/`.

Then `just overview` (or click "Overview" in the dashboard list). The deploy marker — red vertical line at the moment `just break` ran — is on every panel. Latency and errors clearly spike right after it.

Talking point: *"The alert told us **what**. The deploy marker tells us **when**. Now we need **why** — and **traces** answer that."*

### Scene 4 · Heatmap → trace (exemplars)

On the Overview, look at the **Latency heatmap** (bottom-left). It has magenta dots — each dot is a trace exemplar. Click any dot in the dark/slow region.

Tempo opens, the trace is right there. Waterfall shows:
- `validate-input` ✓ ~2 ms
- `cache.check` ✓ ~5 ms
- `db.lookup` 🔴 800 ms — the outlier
- `compute` 🟠 elevated

Click the `db.lookup` span. Resource attributes show `service.version=v1.4.2`, `deployment.environment=production`, `cloud.region=eu-west-1`. Span attributes show `db.concurrency=N`, `chaos.enabled=true`, and the `db.slow_query` event.

Talking point: *"This is metrics-to-trace correlation. The histogram bucket and the trace are the same data point — the exemplar wires them together."*

### Scene 5 · Span → logs → profile

In the span detail panel:
- Click **"Logs for this span"** — Loki opens with the `WARN slow db query` line, including the same trace_id.
- Click **"Profile for this span"** (or open the Profiling dashboard) — the Pyroscope flamegraph shows `worker` CPU. A dominant frame on `expensiveMarshalLoop` jumps out — the regression introduced by `worker v1.4.3`.

Talking point: *"Three signals, one click between them. This is the practice that turns 'where do we even start?' into 'oh, that function.'"*

### Scene 6 · Fix it — watch recovery

```sh
just fix
```

Annotates `worker v1.4.4 (fix)` and disables chaos. Back on the SLO dashboard:
- Within ~30 s the burn rate (5m) drops.
- Within ~90 s the error budget bar starts climbing back.
- The Fast budget burn alert auto-resolves.

End on green.

---

## Commands

```sh
just up              # start the stack
just down            # stop everything
just open            # SLO dashboard (home view)
just overview        # Overview dashboard
just profiling       # Pyroscope flamegraph

just break           # simulate a bad deploy (latency + errors + hot loop)
just fix             # roll back the bad deploy
just deploy <svc> <v>  # just place a deploy annotation (no fault injection)

just explore-traces  # Tempo
just explore-logs    # Loki
just explore-metrics # Prometheus

just logs            # tail api logs
just logs worker     # tail worker logs
just logs-all        # tail all services

just rebuild         # rebuild after code changes
just reset           # wipe data and start fresh
```

---

## Dashboard map

| Dashboard | Purpose |
|---|---|
| **SLOs** | Home view. Availability + latency SLIs, error budgets, burn rates. |
| **Overview** | Request rate, error rate, latency heatmap (with exemplars), per-span p99, service map, cache hit ratio. |
| **Profiling** | Worker CPU + allocation flamegraphs from Pyroscope. |

All dashboards show **deploy annotations** as red vertical lines. They share the time picker, so navigating between them keeps your incident window pinned.

---

## Architecture & tech stack

- **Go 1.25** — all three services
- **OpenTelemetry Go SDK** — traces, metrics, and logs via OTLP/gRPC
- **Pyroscope Go SDK** — continuous CPU + memory profiling on worker
- **W3C TraceContext propagation** — distributed traces span all services
- **[grafana/otel-lgtm](https://github.com/grafana/otel-lgtm)** — single image with Grafana, Loki, Tempo, Prometheus, Pyroscope, and an internal OTel Collector

### File layout

```
.
├── api/                       Go gateway service
├── worker/                    Go downstream w/ Pyroscope + chaos toggle
├── loadgen/                   Go traffic generator (sine wave + bursts)
├── prometheus/
│   ├── prometheus.yaml        Custom config that loads recording rules
│   └── recording-rules.yaml   SLO recording rules (SLI + burn rate)
├── grafana/
│   ├── dashboards/
│   │   ├── slo.json           Home view — SLOs + burn rates
│   │   ├── demo.json          Overview — RED metrics, heatmap, service map
│   │   └── profiling.json     Pyroscope flamegraphs
│   └── provisioning/
│       ├── alerting/          Multi-window burn-rate alert rules
│       ├── dashboards/        Dashboard provisioning
│       └── datasources/       Prometheus / Tempo / Loki / Pyroscope with cross-links
├── docs/runbooks/             Markdown playbooks linked from alerts
├── docker-compose.yml
└── justfile                   `just break` / `just fix` / `just deploy`
```

---

## What this demo deliberately doesn't include

To keep the demo focused and runnable on a laptop:

- **No real Postgres / Redis / Kafka** — the simulated DB inside worker is enough to produce realistic-looking signals.
- **No separate OTel Collector tier** — otel-lgtm contains a collector internally. In production you'd typically run one in front; the practices it enables (tail sampling, redaction) are mentioned in the runbooks.
- **No HA / persistence / auth** — this is a demo on a laptop.
- **No real frontend** — RUM is a great topic for another demo.
