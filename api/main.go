package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

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

const serviceName = "api"

// fakeUsers is a small pool so traces can be filtered by user in Loki/Grafana.
var fakeUsers = []string{"alice", "bob", "charlie", "diana", "eve"}

var (
	tracer       = otel.Tracer(serviceName)
	requestCount metric.Int64Counter
	requestDur   metric.Float64Histogram
	workerURL    string
	workerClient *http.Client
)

// traceContextHandler injects trace_id/span_id into every log record.
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

	otlpHandler := otelslog.NewHandler(serviceName)
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(traceContextHandler{fanoutHandler{otlpHandler, stdoutHandler}})
	slog.SetDefault(logger)

	workerURL = getenv("WORKER_URL", "http://worker:9090")
	workerClient = &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   5 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", instrument("/healthz", healthz))
	mux.HandleFunc("GET /order", instrument("/order", orderHandler))

	handler := otelhttp.NewHandler(mux, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)

	port := getenv("PORT", "8080")
	srv := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		logger.Info("api listening", "port", port, "worker", workerURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func instrument(route string, h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h(sw, r)
		dur := time.Since(start).Seconds()
		requestCount.Add(r.Context(), 1,
			metric.WithAttributes(
				semconv.HTTPRoute(route),
				semconv.HTTPResponseStatusCode(sw.status),
			))
		requestDur.Record(r.Context(), dur,
			metric.WithAttributes(semconv.HTTPRoute(route)))
		slog.LogAttrs(r.Context(), levelFor(sw.status), "request",
			slog.String("route", route),
			slog.String("status", strconv.Itoa(sw.status)),
			slog.Float64("duration_s", dur),
		)
	}
}

func levelFor(status int) slog.Level {
	if status >= 500 {
		return slog.LevelError
	}
	return slog.LevelInfo
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

func orderHandler(w http.ResponseWriter, r *http.Request) {
	orderID := r.URL.Query().Get("id")
	if orderID == "" {
		orderID = fmt.Sprintf("ord-%d", rand.Intn(9000)+1000)
	}
	userID := fakeUsers[rand.Intn(len(fakeUsers))]

	// Enrich the incoming server span.
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.String("order.id", orderID),
		attribute.String("user.id", userID),
	)

	// Child span: validate-input (always fast).
	ctx, validateSpan := tracer.Start(r.Context(), "validate-input",
		trace.WithAttributes(attribute.String("order.id", orderID)))
	time.Sleep(time.Duration(1+rand.Intn(3)) * time.Millisecond)
	validateSpan.End()

	// Call worker — trace context propagated via otelhttp transport.
	workerReq, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/process?id=%s&user=%s", workerURL, orderID, userID), nil)
	resp, err := workerClient.Do(workerReq)
	if err != nil {
		span.SetStatus(codes.Error, "worker unreachable")
		slog.ErrorContext(ctx, "worker call failed", "order_id", orderID, "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 500 {
		span.SetStatus(codes.Error, "worker returned error")
		slog.ErrorContext(ctx, "worker error response",
			"order_id", orderID,
			"user_id", userID,
			"worker_status", resp.StatusCode,
		)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	slog.InfoContext(ctx, "order complete", "order_id", orderID, "user_id", userID)
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
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
	// Override the default histogram buckets — the SDK's defaults assume
	// milliseconds (0..10000) but we record in seconds, which would put
	// every observation in the le=0 bucket.
	latencyView := sdkmetric.NewView(
		sdkmetric.Instrument{Name: "http_request_duration_seconds"},
		sdkmetric.Stream{
			Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
		},
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(10*time.Second))),
		sdkmetric.WithResource(res),
		sdkmetric.WithView(latencyView),
	)
	otel.SetMeterProvider(mp)

	meter := mp.Meter(serviceName)
	requestCount, err = meter.Int64Counter("http_requests_total",
		metric.WithDescription("Total HTTP requests served"))
	if err != nil {
		return nil, err
	}
	requestDur, err = meter.Float64Histogram("http_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithUnit("s"))
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
