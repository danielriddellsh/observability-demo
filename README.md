# Observability Demo

A live "spotting the problem" demo built on Go + OpenTelemetry. Three microservices talk to each other, produce realistic traffic with natural latency spikes and errors, and send **traces**, **logs**, and **metrics** to a single Grafana stack — all in Docker.

> **The point of the demo:** something is quietly broken. The metrics show it, the logs describe it, and the traces pinpoint exactly which service, which operation, and why. You walk through all three in under five minutes.

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
just up        # build images, start stack, wait for Grafana
just open      # open the pre-built dashboard in your browser
```

Traffic starts flowing immediately — no extra setup needed.

---

## What's running

```
loadgen  ──►  api  ──►  worker  ──►  (simulated database)
```

| Service | What it does |
|---|---|
| **loadgen** | Fires requests at 2–10 req/s in a continuous wave, with occasional traffic bursts |
| **api** | The HTTP gateway. Validates the request, then calls worker |
| **worker** | Downstream processor that talks to a simulated database — this is where the interesting behaviour lives |
| **Grafana** | Collects all telemetry and shows it at [localhost:3000](http://localhost:3000) |

Every `/order` request produces a **6-span distributed trace** across both services:

```
GET /order            [loadgen → api]
 └─ GET /order        [api server]
       ├─ validate-input     ~2 ms, always fast
       └─ worker call        [api → worker]
             └─ GET /process [worker server]
                   ├─ cache.check   ~5 ms, always fast
                   ├─ db.lookup     ← where things go wrong
                   └─ compute       ~10 ms, always fast
```

The `db.lookup` operation behaves like a real database under load:

| Frequency | What happens | How it shows up |
|---|---|---|
| ~77% | Fast (10–50 ms) | Normal latency |
| ~15% | Slow query (0.5–1.5 s) | p99 spike · `WARN` log · `db.slow_query` span event |
| ~8% | Error: connection refused | 502 from api · `ERROR` log · red span in Tempo |

As traffic increases (during busy periods), slow query probability and queue latency both increase — just like a real database under load.

---

## Demo walkthrough

This is a three-act story: **metrics show something is wrong → traces show where → logs explain why.**

Allow about **5–10 minutes** for a relaxed run-through.

---

### Act 1 — The dashboard: something doesn't look right

Open the dashboard (`just open`). Take a moment to orientate:

**Top-left — Request Rate**
Traffic naturally rises and falls in waves. Occasional bursts spike it higher. This is the load generator simulating real-world traffic patterns — it's not flat because real traffic never is.

**Top-right — Error Rate %**
There's a small but persistent error rate even during quiet periods (~8%). During busy periods it climbs. A healthy system should be at or near 0%.

**Bottom-left — End-to-end Latency (p50 / p95 / p99)**
The p50 (median user) is fast. The p99 occasionally jumps to 1–2 seconds. That gap means *most* users are fine but *some* are getting a terrible experience — and it correlates with the busy traffic periods.

**Bottom-right — Latency by Span**
Four lines, one per internal operation. Three of them stay near the bottom. One — `db.lookup` — is visibly higher and spikier than all the others.

> **The question you've just raised:** we can see *that* something is slow and *roughly where* in the call chain — now let's prove it with traces.

---

### Act 2 — Traces: find the smoking gun

Open Explore → Tempo (`just explore-traces`), or click any data point on the latency panel and choose **"View slow traces in Tempo →"** from the menu.

**Find a slow trace:**
1. Set Service: `worker`
2. Set Min duration: `500ms`
3. Hit **Search**

**Open one of the results.** You'll see a waterfall:
- `validate-input` ✅ ~2 ms
- `cache.check` ✅ ~5 ms
- `db.lookup` 🔴 800 ms – 1.5 s ← the culprit is obvious
- `compute` ✅ ~10 ms

**Click the `db.lookup` span.** The right panel shows:
- `db.system: postgresql`
- `db.operation: SELECT`
- A span event — `db.slow_query` — with `threshold_ms: 200` and the actual duration recorded
- `db.concurrency: N` — showing how many requests were in-flight simultaneously

At the bottom of the span detail panel there's a **Logs** button — click it to jump straight to the Loki logs for this exact trace, no copy-pasting needed.

**Find an error trace:**
- Change Status to `error` and search again
- The `db.lookup` span is red with an `exception` event: `db: connection refused`

> **The story so far:** traces tell us the database is the problem, and the span attributes tell us *why* (queue depth, high concurrency). Now let's look at the logs for the same moment.

---

### Act 3 — Logs: confirm and correlate

Open Explore → Loki (`just explore-logs`).

**Slow query warnings:**
```
{service_name="worker"} | json | msg="slow db query"
```
Each line is a structured log:
```json
{ "level": "WARN", "msg": "slow db query", "duration_ms": 1243, "queue_depth": 4, "trace_id": "abc123..." }
```

**Error logs:**
```
{service_name="worker"} | json | level="ERROR"
```

**The magic moment — clicking a `trace_id`:**

Every log line includes the `trace_id` from the active request. Click the value — Grafana jumps directly to the matching trace in Tempo. Same slow `db.lookup` span, same moment in time, now viewed from two angles.

> **This is the payoff:** a log fires the alert, the `trace_id` in the log takes you straight to the exact moment in the distributed call, and the span attributes explain the root cause — all without SSH-ing into anything or grepping through files.

---

### Summary: the three questions, answered

| Question | Tool | Answer |
|---|---|---|
| Is something broken? | Metrics dashboard | Error rate ~8%, p99 spikes to 1–2 s |
| Which service? Which operation? | Traces (Tempo waterfall) | `worker` · `db.lookup` span |
| Why? | Span attributes | High concurrency → queue depth → slow/failed queries |
| When exactly? | Logs (Loki) | Timestamped `WARN`/`ERROR` with duration and queue depth |
| Same incident as the trace? | `trace_id` in logs | One click links the log line directly to the trace |

---

## Commands

```sh
just up              # start the full stack (builds if needed)
just down            # stop everything
just open            # open the Grafana dashboard
just explore-traces  # open Tempo in Grafana Explore
just explore-logs    # open Loki in Grafana Explore
just explore-metrics # open Prometheus in Grafana Explore
just logs            # stream api logs
just logs worker     # stream worker logs
just logs-all        # stream all service logs at once
just rebuild         # rebuild images after code changes
just reset           # wipe all data and start completely fresh
```

---

## Grafana query cheat sheet

| What you want | Datasource | Query |
|---|---|---|
| Request rate | Prometheus | `sum by (http_route) (rate(http_requests_total[1m]))` |
| Error % | Prometheus | `100 * sum(rate(http_requests_total{http_response_status_code=~"5.."}[1m])) / sum(rate(http_requests_total[1m]))` |
| API p99 latency | Prometheus | `histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket[1m])))` |
| DB p99 latency | Prometheus | `histogram_quantile(0.99, sum by (le) (rate(worker_db_duration_seconds_bucket[1m])))` |
| Slow query logs | Loki | `{service_name="worker"} \| json \| msg="slow db query"` |
| Error logs | Loki | `{service_name="worker"} \| json \| level="ERROR"` |
| Traces by order ID | Tempo | Tag: `order.id = ord-0042` |
| Traces by user | Tempo | Tag: `user.id = alice` |
| All slow traces | Tempo | Service: `worker` · Min duration: `500ms` |

---

## Tech stack

- **Go 1.25** — all three services
- **OpenTelemetry Go SDK** — traces, metrics, and logs all emitted over OTLP/gRPC
- **W3C TraceContext propagation** — trace context flows loadgen → api → worker via HTTP headers, producing connected multi-service traces
- **[grafana/otel-lgtm](https://github.com/grafana/otel-lgtm)** — single Docker image with Grafana, Loki, Tempo, Prometheus, and an OTel Collector bundled together. Zero external dependencies.
