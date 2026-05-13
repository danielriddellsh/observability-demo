# Runbook: High error rate

**Alert:** `High error rate`
**Severity:** page
**Triggered when:** more than 5% of requests are returning 5xx for 5 minutes

## First 60 seconds

1. **Overview dashboard → "Error Rate %" panel** — confirm and note when it started.
2. **Look for a deploy marker** in the same window.
3. **Click the panel's "View error traces" data link** — Tempo opens filtered to `status=error`.

## Reading the error traces

Open an error trace. Look at:

- The **failing span's name and service** — is it always the same? (yes = single broken component; no = wider issue)
- The **span events** — `exception` events carry the error message
- The **resource attributes** — `service.version` tells you if it correlates with a release
- The **"Logs for this span"** button — gets you to the error log line in Loki

## Mitigation

| Cause | Action |
|---|---|
| Bad deploy (version correlates) | Roll back: `just deploy <service> <previous-version>` |
| Downstream dependency failing | Check that dependency; consider failing fast / circuit breaking |
| Database connection refused | Verify the DB is reachable; check connection pool size vs concurrency |
| Cascading from another service | Open that service's dashboard and start there instead |

## Verify recovery

Error rate panel returns to baseline (<1% in this demo). The alert auto-resolves after error rate stays below 5% for 5 minutes.
