package main

import (
	"context"
	"fmt"
	"log"
	"time"

	sw "github.com/tauki/scheduledworker"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func metrics(meter metric.Meter) (sw.MetricsCollector, error) {
	counters := make([]metric.Int64Counter, 5)
	for i, name := range []string{"scheduled", "started", "completed", "failed", "panicked"} {
		counter, err := meter.Int64Counter("scheduledworker." + name)
		if err != nil {
			return nil, err
		}
		counters[i] = counter
	}
	wait, err := meter.Float64Histogram("scheduledworker.wait", metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	duration, err := meter.Float64Histogram("scheduledworker.run", metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	return sw.MetricsFuncs{
		Scheduled: func() { counters[0].Add(ctx, 1) }, Started: func() { counters[1].Add(ctx, 1) }, Completed: func() { counters[2].Add(ctx, 1) }, Failed: func() { counters[3].Add(ctx, 1) }, Panicked: func() { counters[4].Add(ctx, 1) },
		Wait: func(d time.Duration) { wait.Record(ctx, d.Seconds()) }, Run: func(d time.Duration) { duration.Record(ctx, d.Seconds()) },
	}, nil
}
func run() error {
	ctx := context.Background()
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return err
	}
	tracing := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tracing.Shutdown(ctx) }()
	reader := sdkmetric.NewManualReader()
	metering := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = metering.Shutdown(ctx) }()
	collector, err := metrics(metering.Meter("scheduledworker-example"))
	if err != nil {
		return err
	}
	tracer := tracing.Tracer("scheduledworker-example")
	middleware := sw.WithOnRun(func(ctx context.Context, task *sw.Task, next sw.Next) error {
		ctx, span := tracer.Start(ctx, "scheduled task", trace.WithAttributes(attribute.Int("attempt", task.Spec().Attempt)))
		defer span.End()
		err := next(ctx, task)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	})
	w := sw.New().Start()
	defer w.Stop()
	if err := w.SubmitWithOptions(sw.Task{FnErr: func(context.Context) error { return nil }}, middleware, sw.WithMetrics(collector)); err != nil {
		return err
	}
	w.Stop()
	var snapshot metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &snapshot); err != nil {
		return err
	}
	if len(snapshot.ScopeMetrics) == 0 {
		return fmt.Errorf("no scheduler metrics collected")
	}
	fmt.Println("OpenTelemetry: span exported and scheduler metrics collected")
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
