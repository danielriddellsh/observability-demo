# Runbook: SLO slow budget burn

**Alert:** `Availability: slow budget burn`
**Severity:** ticket (not page)
**Triggered when:** burn rate > 6 over both a 6h and a 30m window

## What this means

Persistent low-grade degradation. The budget is being eaten faster than allowed but not catastrophically — at this rate the 30-day budget runs out in about 5–10 days. No customer-impacting outage, but the system is unhealthy.

This is the kind of thing a fast-burn alert misses because no single window is dramatic, but the trend is real.

## First steps

1. **Open the [SLO dashboard](http://localhost:3000/d/slo)** — note when the slow burn started.
2. **Open the [Overview dashboard](http://localhost:3000/d/obs-demo)**, time range "last 24 hours" — look for the inflection point.
3. **Correlate with deploys** — has every release in the last day shipped slower or buggier code?

## Common causes (in order of likelihood)

1. **A regression that slipped past tests.** Find the deploy and diff the code.
2. **A downstream that's slowly degrading** (e.g. database needing vacuum, cache hit ratio drifting down).
3. **Traffic growth** outpacing capacity — same error rate but more total errors.
4. **A noisy neighbour** in a shared environment.

## Mitigation

This isn't an emergency. File a ticket, link this dashboard, and prioritise in the next sprint. If the burn rate climbs into "fast burn" territory, the page-severity alert will fire and escalate.
