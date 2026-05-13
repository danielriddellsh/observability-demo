# Observability demo — run `just` to see available commands.

# Start the full stack (build if needed).
up:
    docker-compose up --build -d
    @echo ""
    @echo "Waiting for Grafana..."
    @until curl -sf http://localhost:3000/api/health >/dev/null; do sleep 1; done
    @echo "Stack is up. Open Grafana with: just open"

# Stop and remove all containers.
down:
    docker-compose down

# Rebuild images and restart (useful after code changes).
rebuild:
    docker-compose up --build -d

# Open Grafana on the SLO dashboard (the demo's home view).
open:
    open "http://localhost:3000/d/slo/slos-service-level-objectives"

# Open the Overview dashboard.
overview:
    open "http://localhost:3000/d/obs-demo/overview-observability-demo"

# Open the Profiling dashboard.
profiling:
    open "http://localhost:3000/d/profiling/profiling-worker-flamegraph"

# Open Grafana Explore for traces, logs, or metrics.
explore-traces:
    open "http://localhost:3000/explore?left=%7B%22datasource%22:%22tempo%22%7D"

explore-logs:
    open "http://localhost:3000/explore?left=%7B%22datasource%22:%22loki%22%7D"

explore-metrics:
    open "http://localhost:3000/explore?left=%7B%22datasource%22:%22prometheus%22%7D"

# Stream logs from a specific service (default: api).
# Usage: just logs        (api)
#        just logs worker
#        just logs loadgen
logs service="api":
    docker-compose logs -f {{service}}

# Tail logs from all services at once.
logs-all:
    docker-compose logs -f

# Show running container status.
ps:
    docker-compose ps

# Wipe everything including volumes and start fresh.
reset:
    docker-compose down -v
    docker-compose up --build -d

# ──────────────────────────────────────────────────────────────────────────
# Live demo recipes — these are what you run during a presentation.
# ──────────────────────────────────────────────────────────────────────────

# Place a deploy annotation on every dashboard. Use during the demo to
# show change-correlation: latency spike right after the marker = bad deploy.
#
# Usage: just deploy worker v1.4.3
deploy service version:
    @curl -s -X POST http://localhost:3000/api/annotations \
        -H "Content-Type: application/json" \
        -d '{"text":"deploy: {{service}} {{version}}","tags":["deploy","{{service}}","{{version}}"]}' \
        | grep -o '"id":[0-9]*' || true
    @echo "📍 Marked deploy: {{service}} {{version}}"

# Inject a synthetic regression in worker — latency spike + extra errors.
# Place a deploy marker first so the dashboard tells the story.
# This is "ship a bad release" in one command.
break:
    @curl -s -X POST http://localhost:3000/api/annotations \
        -H "Content-Type: application/json" \
        -d '{"text":"deploy: worker v1.4.3 (regression)","tags":["deploy","worker","v1.4.3"]}' >/dev/null
    @curl -s -X POST "http://localhost:9090/admin/chaos?on=true" >/dev/null
    @echo "💥 Chaos enabled — worker v1.4.3 'deployed'. Watch the SLO dashboard."

# Remove the synthetic regression. Like rolling back a bad deploy.
fix:
    @curl -s -X POST http://localhost:3000/api/annotations \
        -H "Content-Type: application/json" \
        -d '{"text":"deploy: worker v1.4.4 (fix)","tags":["deploy","worker","v1.4.4"]}' >/dev/null
    @curl -s -X POST "http://localhost:9090/admin/chaos?on=false" >/dev/null
    @echo "✅ Chaos disabled — worker v1.4.4 'deployed'. Recovery within ~60s."
