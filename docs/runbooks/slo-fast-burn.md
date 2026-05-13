# Runbook: SLO fast budget burn

**Alert:** `Availability: fast budget burn` or `Latency: fast budget burn`
**Severity:** page
**Triggered when:** burn rate > 14.4 over both a 1h and a 5m window

## What this means

We're burning the 30-day error budget more than 14× faster than allowed. If this continues for two days, the entire month's budget is gone. This is a "wake up at 3am" alert — something tangible is broken.

## First 60 seconds

1. **Open the [SLO dashboard](http://localhost:3000/d/slo)** — confirm both 1h and 5m burn rates are red. If only one window is hot, you may be looking at a brief blip.
2. **Open the [Overview dashboard](http://localhost:3000/d/obs-demo)** — eyeball the request rate, error rate, and latency panels for the moment things changed.
3. **Look for deploy markers** (red vertical lines on every dashboard) — anything land in the last 30 minutes?

## Likely causes

| Symptom | Most likely cause | Where to look next |
|---|---|---|
| Error rate spiked, latency normal | Bad deploy returning 5xx | Service detail dashboard → click latest deploy marker |
| Latency spiked, error rate normal | Downstream dependency slow (DB, cache) | Service map panel — find the red edge |
| Both spiked together | Hard outage in a dependency | Tempo: search for traces with `status=error`, look at the failing span |
| Spike correlates with traffic burst | Capacity / queue exhaustion | Check `db.concurrency` on `db.lookup` spans — if it's high, scale or shed load |

## Confirm the root cause with traces

1. On the Overview dashboard, click any datapoint on the latency or error rate panel
2. The "View error traces" / "View slow traces" data link drops you into Tempo, time-window pinned
3. Open a trace — the `db.lookup` span is usually the outlier
4. Click "Logs for this span" — `WARN`/`ERROR` lines confirm the failure mode
5. Click "Profile for this span" — flamegraph shows what was hot

## Mitigation

- **If recent deploy:** roll back. `just deploy <service> <previous-version>`
- **If load spike:** loadgen burst is finite; if real-world traffic, throttle upstream
- **If downstream:** check the failing dependency's own dashboard
- **If unsure:** silence the alert for 30 minutes while you investigate, but only after confirming you're actively looking

## Resolution

Alert auto-resolves once burn rate drops below threshold on both windows. Watch the SLO dashboard's burn-rate panels return to green.
