# Runbook: Latency degradation

**Alert:** `Latency: fast budget burn`
**Severity:** page
**Triggered when:** too many `/order` requests are exceeding the 500ms target

## What this means

Some fraction of users are getting noticeably slow responses. P99 is likely above 1s.

## First 60 seconds

1. **Overview dashboard → "End-to-end Latency" panel** — confirm p99 is elevated.
2. **"p99 Latency by Span" panel** — which span is the outlier?
   - `db.lookup` → database problem
   - `cache.check` → cache miss storm
   - `validate-input` → very unusual; likely a CPU issue in `api`
   - `compute` → check the worker flamegraph in Pyroscope

## Drill in

1. Click on the latency heatmap exemplar dots to jump straight to a slow trace.
2. In the trace, the outlier span's attributes tell the story:
   - `db.concurrency > 5` → connection pool pressure; check load
   - `cache.hit = false` → cache miss; check Redis health
   - Anything else → check the span's logs and profile

## Mitigation

- **Database under pressure:** shed load (rate-limit, drop non-essential traffic) until concurrency comes down.
- **Cache misses:** check whether the cache was restarted or evicted. If a hot key is missing, pre-warm it.
- **Bad deploy:** roll back.

## Verify recovery

The p99 should drop within 1–2 minutes of mitigation. The burn rate panels on the SLO dashboard return to green when the SLI recovers.
