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

# Open the Observability Demo dashboard in your browser.
open:
    open "http://localhost:3000/d/obs-demo/observability-demo"

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
