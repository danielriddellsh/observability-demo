package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const serviceName = "loadgen"

// tickDelay returns a variable inter-request delay that creates a natural
// traffic wave visible on the Grafana dashboard.
//
// Shape: two superimposed sine waves produce a gently rolling rate that
// never looks mechanical. The primary wave has a 8-minute period; a slower
// secondary wave (25-minute period, 30% weight) adds a long-term drift.
// Together they swing the rate between ~2.5 req/s and ~7 req/s.
// Every ~40 s a short burst fires extra requests to simulate a real-world spike.
func tickDelay() time.Duration {
	t := float64(time.Now().Unix())

	// Primary wave: 8-minute period
	primary := math.Sin(t / 480.0 * 2 * math.Pi)
	// Secondary drift: 25-minute period at 30% weight
	secondary := math.Sin(t/1500.0*2*math.Pi) * 0.3

	// Combine and normalise to [0, 1]
	wave := ((primary + secondary) / 1.3 + 1) / 2

	// Map wave → delay: high wave = low delay (busy), low wave = high delay (quiet)
	base := 140 + int(wave*290) // 140 ms (busy) … 430 ms (quiet)

	// Small per-tick jitter ±15 ms
	jitter := rand.Intn(30) - 15

	return time.Duration(base+jitter) * time.Millisecond
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdown, err := setupTracing(ctx)
	if err != nil {
		logger.Error("otel setup", "err", err)
		os.Exit(1)
	}
	defer shutdown(context.Background())

	target := getenv("TARGET", "http://api:8080")
	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	}

	logger.Info("loadgen starting", "target", target)

	var orderSeq int
	// nextBurst tracks when to fire the next traffic spike.
	nextBurst := time.Now().Add(time.Duration(30+rand.Intn(30)) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tickDelay()):
			orderSeq++
			orderID := fmt.Sprintf("ord-%04d", orderSeq)
			go hit(ctx, client, fmt.Sprintf("%s/order?id=%s", target, orderID))

			// Periodic burst: fire 5 extra requests in quick succession.
			if time.Now().After(nextBurst) {
				logger.Info("firing traffic burst")
				for i := range 5 {
					orderSeq++
					oid := fmt.Sprintf("ord-%04d", orderSeq)
					delay := time.Duration(i*30) * time.Millisecond
					go func(id string, d time.Duration) {
						time.Sleep(d)
						hit(ctx, client, fmt.Sprintf("%s/order?id=%s", target, id))
					}(oid, delay)
				}
				nextBurst = time.Now().Add(time.Duration(30+rand.Intn(30)) * time.Second)
			}
		}
	}
}

func hit(ctx context.Context, client *http.Client, url string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "request failed", "url", url, "err", err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	slog.InfoContext(ctx, "request", "url", url, "status", resp.StatusCode)
}

func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(getenv("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(getenv("SERVICE_VERSION", "v1.4.2")),
			semconv.DeploymentEnvironment(getenv("DEPLOYMENT_ENV", "production")),
			attribute.String("cloud.region", getenv("CLOUD_REGION", "eu-west-1")),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
	)
	if err != nil {
		return nil, err
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
