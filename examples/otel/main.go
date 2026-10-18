package main

import (
	"context"
	"fmt"

	sw "github.com/tauki/scheduledworker/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	ctx := context.Background()
	exporter, err := stdouttrace.New()
	if err != nil {
		panic(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(ctx)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer mp.Shutdown(ctx)
	s, err := sw.New(sw.Config{})
	if err != nil {
		panic(err)
	}
	if err = s.Start(ctx); err != nil {
		panic(err)
	}
	meter := mp.Meter("scheduledworker-v2-example")
	succeeded, err := meter.Int64ObservableCounter("scheduledworker.v2.runs.succeeded")
	if err != nil {
		panic(err)
	}
	registration, err := meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		o.ObserveInt64(succeeded, int64(s.Status().Counters.RunsSucceeded))
		return nil
	}, succeeded)
	if err != nil {
		panic(err)
	}
	defer registration.Unregister()
	tracer := tp.Tracer("scheduledworker-v2-example")
	work := func(context.Context, sw.RunInfo) error { return nil }
	// A handler decorator observes invocation, not coordinator-final outcomes.
	// Metadata is immutable application-owned data; no per-ID registry is retained.
	h, err := s.Schedule(ctx, sw.Spec{Name: "refresh"}, func(ctx context.Context, r sw.RunInfo) error {
		ctx, span := tracer.Start(ctx, "refresh", trace.WithAttributes(attribute.String("schedule.id", string(r.ScheduleID)), attribute.String("run.id", string(r.RunID)), attribute.Int("attempt", r.Attempt)))
		defer span.End()
		err := work(ctx, r)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	})
	if err != nil {
		panic(err)
	}
	if _, err = h.Wait(ctx); err != nil {
		panic(err)
	}
	if err = s.Shutdown(ctx); err != nil {
		panic(err)
	}
	var data metricdata.ResourceMetrics
	if err = reader.Collect(ctx, &data); err != nil {
		panic(err)
	}
	found := false
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, p := range sum.DataPoints {
					if p.Value == 1 {
						found = true
					}
				}
			}
		}
	}
	if !found {
		panic("authoritative run counter missing")
	}
	fmt.Println("OpenTelemetry: invocation span and authoritative run counter exported")
}
