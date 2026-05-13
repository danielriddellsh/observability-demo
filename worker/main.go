package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceName = "worker"

var (
	tracer      = otel.Tracer(serviceName)
	dbDuration  metric.Float64Histogram
	dbErrors    metric.Int64Counter
	cacheOps    metric.Int64Counter
	activeCalls atomic.Int32 // in-flight /process requests
	chaosMode   atomic.Bool  // toggled via /admin/chaos endpoint; injects extra latency + errors
)

type traceContextHandler struct{ slog.Handler }

func (h traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

type fanoutHandler []slog.Handler

func (f fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range f {
		_ = h.Handle(ctx, r.Clone())
	}
	return nil
}
func (f fanoutHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(as)
	}
	return out
}
func (f fanoutHandler) WithGroup(name string) slog.Handler {
	out := make(fanoutHandler, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdown, err := setupOTel(ctx)
	if err != nil {
		slog.Error("otel setup failed", "err", err)
		os.Exit(1)
	}
	defer shutdown(context.Background())

	// Continuous profiling — sends CPU/memory profiles to Pyroscope (port 4040 in otel-lgtm).
	profiler, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: serviceName,
		ServerAddress:   getenv("PYROSCOPE_URL", "http://otel-lgtm:4040"),
		Tags: map[string]string{
			"region":      getenv("CLOUD_REGION", "eu-west-1"),
			"environment": getenv("DEPLOYMENT_ENV", "production"),
			"version":     getenv("SERVICE_VERSION", "v1.4.2"),
		},
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
		},
	})
	if err != nil {
		slog.Warn("pyroscope start failed (continuing without profiling)", "err", err)
	} else {
		defer profiler.Stop()
	}

	otlpHandler := otelslog.NewHandler(serviceName)
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(traceContextHandler{fanoutHandler{otlpHandler, stdoutHandler}})
	slog.SetDefault(logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /process", processHandler)
	// Chaos toggle used by `just break` / `just fix` to simulate a bad deploy.
	mux.HandleFunc("POST /admin/chaos", func(w http.ResponseWriter, r *http.Request) {
		on := r.URL.Query().Get("on") == "true"
		chaosMode.Store(on)
		slog.WarnContext(r.Context(), "chaos mode toggled", "enabled", on)
		fmt.Fprintf(w, `{"chaos":%v}`, on)
	})

	handler := otelhttp.NewHandler(mux, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)

	port := getenv("PORT", "9090")
	srv := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		logger.Info("worker listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func processHandler(w http.ResponseWriter, r *http.Request) {
	activeCalls.Add(1)
	defer activeCalls.Add(-1)
	orderID := r.URL.Query().Get("id")
	userID := r.URL.Query().Get("user")

	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String("order.id", orderID),
		attribute.String("user.id", userID),
	)

	// Log at entry so every span has at least one log line anchored to the start.
	slog.InfoContext(r.Context(), "processing order", "order_id", orderID, "user_id", userID)

	if err := cacheCheck(r.Context(), orderID); err != nil {
		slog.ErrorContext(r.Context(), "cache check failed", "order_id", orderID, "err", err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "cache error", http.StatusInternalServerError)
		return
	}

	if err := dbLookup(r.Context(), orderID, userID); err != nil {
		slog.ErrorContext(r.Context(), "db lookup failed", "order_id", orderID, "err", err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if err := compute(r.Context(), orderID); err != nil {
		slog.ErrorContext(r.Context(), "compute failed", "order_id", orderID, "err", err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "compute error", http.StatusInternalServerError)
		return
	}

	slog.InfoContext(r.Context(), "order processed", "order_id", orderID, "user_id", userID)
	fmt.Fprintf(w, `{"order_id":%q,"status":"ok"}`, orderID)
}

// cacheCheck simulates a Redis lookup. ~80% hit ratio normally, dropping
// further when load is high (mirrors a warm cache being evicted under pressure).
func cacheCheck(ctx context.Context, orderID string) error {
	concurrency := int(activeCalls.Load())
	_, span := tracer.Start(ctx, "cache.check",
		trace.WithAttributes(
			attribute.String("cache.backend", "redis"),
			attribute.String("cache.key", "order:"+orderID),
			attribute.Int("cache.ttl_seconds", 300),
			attribute.String("order.id", orderID),
		),
	)
	defer span.End()

	// Miss probability rises with load (hot keys evicted under pressure).
	missProb := 0.20 + float64(max(0, concurrency-3))*0.03
	hit := rand.Float64() >= missProb

	if hit {
		time.Sleep(time.Duration(2+rand.Intn(5)) * time.Millisecond)
		span.SetAttributes(attribute.Bool("cache.hit", true))
		cacheOps.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "hit")))
	} else {
		// Miss: slower round-trip simulating a fallback to disk/cold cache.
		time.Sleep(time.Duration(15+rand.Intn(35)) * time.Millisecond)
		span.SetAttributes(attribute.Bool("cache.hit", false))
		span.AddEvent("cache.miss", trace.WithAttributes(
			attribute.String("cache.key", "order:"+orderID),
		))
		cacheOps.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "miss")))
	}
	return nil
}

// dbLookup simulates a real-world database call whose behaviour degrades
// under load — mirroring queuing theory (Little's Law):
//
//   - Base rates: ~98% fast, ~1.5% slow query, ~0.5% error (within a 99% SLO)
//   - Each extra concurrent request above 3 adds ~40 ms of queuing latency
//     and pushes the slow/error probability up
//   - When chaos mode is on (toggled via /admin/chaos), baseline errors jump
//     to ~25% and an extra 300 ms of latency is added — simulating a bad deploy.
func dbLookup(ctx context.Context, orderID, userID string) error {
	concurrency := int(activeCalls.Load())
	chaos := chaosMode.Load()

	ctx, span := tracer.Start(ctx, "db.lookup",
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.name", "orders"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("order.id", orderID),
			attribute.String("user.id", userID),
			attribute.Int("db.concurrency", concurrency),
			attribute.Bool("chaos.enabled", chaos),
		),
	)
	defer span.End()

	start := time.Now()

	// Queuing overhead: starts once concurrency exceeds the simulated pool size (3).
	queueDepth := max(0, concurrency-3)
	queueLatency := time.Duration(queueDepth*40) * time.Millisecond

	// Baseline probabilities stay well inside a 99% SLO budget.
	// Load-correlated escalation pushes them up under pressure.
	errorProb := 0.005 + float64(queueDepth)*0.015
	slowProb := 0.015 + float64(queueDepth)*0.02

	// Chaos amplifies everything to simulate a bad deploy.
	if chaos {
		errorProb += 0.25
		slowProb += 0.20
		queueLatency += 300 * time.Millisecond
	}

	r := rand.Float64()
	var err error

	switch {
	case r < errorProb:
		time.Sleep(queueLatency + time.Duration(10+rand.Intn(20))*time.Millisecond)
		err = errors.New("db: connection refused")
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("exception", trace.WithAttributes(
			attribute.String("exception.type", "DatabaseConnectionError"),
			attribute.String("exception.message", err.Error()),
		))
		dbErrors.Add(ctx, 1)

	case r < errorProb+slowProb:
		d := queueLatency + time.Duration(500+rand.Intn(1000))*time.Millisecond
		time.Sleep(d)
		span.AddEvent("db.slow_query", trace.WithAttributes(
			attribute.Int64("threshold_ms", 200),
			attribute.Int64("actual_ms", d.Milliseconds()),
			attribute.Int("queue_depth", queueDepth),
		))
		slog.WarnContext(ctx, "slow db query",
			"order_id", orderID,
			"duration_ms", d.Milliseconds(),
			"queue_depth", queueDepth,
		)

	default:
		time.Sleep(queueLatency + time.Duration(10+rand.Intn(40))*time.Millisecond)
	}

	dbDuration.Record(ctx, time.Since(start).Seconds())
	return err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func compute(ctx context.Context, orderID string) error {
	_, span := tracer.Start(ctx, "compute",
		trace.WithAttributes(attribute.String("order.id", orderID)),
	)
	defer span.End()
	time.Sleep(time.Duration(5+rand.Intn(15)) * time.Millisecond)

	// When chaos mode is on, this branch dominates the worker CPU flamegraph
	// in Pyroscope — a real-world example of "a bad release shipped a hot
	// loop into a previously cheap function."
	if chaosMode.Load() {
		expensiveMarshalLoop(orderID)
	}
	return nil
}

// expensiveMarshalLoop simulates a regression introduced by a bad deploy:
// repeatedly serialising a payload that should have been cached. Designed
// to be visible as a dominant frame in the Pyroscope CPU flamegraph.
func expensiveMarshalLoop(orderID string) {
	payload := map[string]any{
		"order_id": orderID,
		"items":    make([]map[string]any, 50),
	}
	for i := range 50 {
		payload["items"].([]map[string]any)[i] = map[string]any{
			"sku":   fmt.Sprintf("SKU-%05d", i),
			"qty":   rand.Intn(10),
			"price": rand.Float64() * 100,
		}
	}
	// 2000 marshal calls per request — clearly visible on a flamegraph
	// without making the trace noticeably slower than the chaos latency hit.
	for range 2000 {
		_, _ = json.Marshal(payload)
	}
}

func setupOTel(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(getenv("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(getenv("SERVICE_VERSION", "v1.4.2")),
			semconv.DeploymentEnvironment(getenv("DEPLOYMENT_ENV", "production")),
			attribute.String("cloud.region", getenv("CLOUD_REGION", "eu-west-1")),
			attribute.String("k8s.cluster.name", getenv("K8S_CLUSTER", "prod-eu-1")),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logExp, err := otlploggrpc.New(ctx, otlploggrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)

	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("metric exporter: %w", err)
	}
	// Override default histogram buckets — defaults assume milliseconds, we use seconds.
	dbLatencyView := sdkmetric.NewView(
		sdkmetric.Instrument{Name: "worker_db_duration_seconds"},
		sdkmetric.Stream{
			Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
		},
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(10*time.Second))),
		sdkmetric.WithResource(res),
		sdkmetric.WithView(dbLatencyView),
	)
	otel.SetMeterProvider(mp)

	meter := mp.Meter(serviceName)
	dbDuration, err = meter.Float64Histogram("worker_db_duration_seconds",
		metric.WithDescription("db.lookup duration in seconds"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	dbErrors, err = meter.Int64Counter("worker_db_errors_total",
		metric.WithDescription("Total db.lookup errors"))
	if err != nil {
		return nil, err
	}
	cacheOps, err = meter.Int64Counter("worker_cache_operations_total",
		metric.WithDescription("Cache operations split by hit/miss"))
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) error {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil
	}, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
