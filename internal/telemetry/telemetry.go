package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// InitOTEL sets up OpenTelemetry tracing if OTEL_EXPORTER_OTLP_ENDPOINT is set.
// Returns a shutdown function that flushes pending spans.
func InitOTEL(ctx context.Context) (func(), error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func() {}, nil
	}

	opts := []otlptracehttp.Option{}

	if strings.EqualFold(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true") {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return func() {}, fmt.Errorf("otel exporter: %w", err)
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "vibecast-cli"
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return func() {}, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tp.Shutdown(shutdownCtx)
	}

	return shutdown, nil
}

// Tracer returns the global tracer for vibecast instrumentation.
func Tracer() trace.Tracer {
	return otel.Tracer("vibecast-cli")
}

// ConfigureOTEL dynamically (re)configures OpenTelemetry on a running CLI.
// oldShutdown is called to shut down any existing provider.
// Returns the new shutdown function.
func ConfigureOTEL(oldShutdown func(), endpoint string, insecure bool, serviceName string) (func(), error) {
	if oldShutdown != nil {
		oldShutdown()
	}

	if endpoint == "" {
		return nil, nil
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
	}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}

	if serviceName == "" {
		serviceName = "vibecast-cli"
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tp.Shutdown(shutdownCtx)
	}

	fmt.Fprintf(os.Stderr, "[otel] Reconfigured: endpoint=%s insecure=%v service=%s\n", endpoint, insecure, serviceName)
	return shutdown, nil
}

// PluginDir returns the absolute path to the claude-plugin directory
// that ships next to the vibecast binary.
func PluginDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	dir := filepath.Join(filepath.Dir(exePath), "claude-plugin")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}
