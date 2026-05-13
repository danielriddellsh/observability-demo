# Observability Demo

A live "spotting the problem" demo on Go + OpenTelemetry + the Grafana stack. Three microservices talk to each other and emit **traces**, **logs**, and **metrics** into a single Grafana instance. Wired-up correlations let you click your way from a metric spike → a trace → a log line in under a minute.

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
just open      # opens the Overview dashboard
```

Traffic starts flowing immediately.

---

## What's running

```
loadgen  ──►  api  ──►  worker  ──►  (simulated Redis cache, Postgres DB)
all three  ──►  otel-lgtm  (Grafana + Loki + Tempo + Prometheus)
```

| Service | Role |
|---|---|
| **loadgen** | Variable-rate traffic generator with sine-wave + bursts |
| **api** | HTTP gateway, propagates trace context to worker |
| **worker** | Downstream service — talks to a simulated cache + DB with load-correlated behaviour |
| **otel-lgtm** | All Grafana telemetry backends in one container |

Every `/order` request produces an **8-span distributed trace** spanning two services, with cache hit/miss variation, queue-depth-correlated DB latency, and a clear `db.lookup` outlier when things degrade.

---

## What's wired up

| Capability | Where to see it |
|---|---|
| **Distributed tracing** | Trace context propagates loadgen → api → worker via W3C headers; 8 spans per request in Tempo |
| **Exemplar-linked metrics** | Latency heatmap shows trace-ID dots; click any dot → opens the trace |
| **Trace ↔ log correlation** | From a span: "Logs for this span" opens Loki filtered to the matching log line |
| **Service map** | Live topology from Tempo span metrics, edge thickness = RPS |
| **Resource-attribute slicing** | Every signal carries `service.version`, `deployment.environment`, `cloud.region`, `k8s.cluster.name` |
| **Structured logs with trace_id** | Every log line has `trace_id` and `span_id`; click → opens the trace |

---

## The demo — three scenes

### Scene 1 · The Overview dashboard

`just open` puts you on the **Overview** dashboard.

- **Top:** request rate, error rate
- **Middle:** latency heatmap (with magenta exemplar dots), per-span p99 breakdown
- **Bottom:** live service map, cache hit ratio

In steady state everything is green and the `db.lookup` line on the per-span panel sits well above the rest — that's the bottleneck and where Scene 2 drills in.

### Scene 2 · Heatmap → trace (exemplars)

Click any magenta dot on the latency heatmap. Tempo opens with the trace waterfall:

- `validate-input` ✓ ~2 ms
- `cache.check` ✓ ~5 ms
- `db.lookup` 🔴 200 ms – 1 s — the outlier
- `compute` ✓ ~10 ms

Click `db.lookup`. Resource attributes show `service.version=v1.4.2`, `deployment.environment=production`, `cloud.region=eu-west-1`. Span attributes include `db.concurrency=N` and the `db.slow_query` event when latency spikes.

### Scene 3 · Trace → logs

In the span detail panel, click **"Logs for this span"**. Loki opens with the `WARN slow db query` line carrying the same trace_id. Same incident, viewed from two angles.

---

## Commands

```sh
just up              # start the stack
just down            # stop everything
just open            # Overview dashboard

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

## Architecture

- **Go 1.25** — all three services
- **OpenTelemetry Go SDK** — traces, metrics, and logs via OTLP/gRPC
- **W3C TraceContext propagation** — distributed traces span all services
- **[grafana/otel-lgtm](https://github.com/grafana/otel-lgtm)** — single image with Grafana, Loki, Tempo, Prometheus, and an internal OTel Collector

### File layout

```
.
├── api/                       Go gateway service
├── worker/                    Go downstream service
├── loadgen/                   Go traffic generator (sine wave + bursts)
├── grafana/
│   ├── dashboards/demo.json   Overview — RED metrics, heatmap, service map, cache
│   └── provisioning/          Datasources with cross-links + dashboard provisioning
├── docker-compose.yml
└── justfile
```
