package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"go.opentelemetry.io/otel/api/global"
	export "go.opentelemetry.io/otel/sdk/export/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setRecorderTracer(sampleRate float64) (func(), error) {
	exporter, err := NewRecorder()
	if err != nil {
		return nil, err
	}

	tp, err := sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.ProbabilitySampler(sampleRate)}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		return nil, err
	}
	global.SetTraceProvider(tp)
	return func() {}, nil
}

// Exporter is an implementation of trace.Exporter that writes spans to stdout.
type recorderExporter struct {
	outputWriter io.Writer
	errorFunc    errorFunc
}

func NewRecorder() (*recorderExporter, error) {
	ef := func(err error) {

	}
	return &recorderExporter{&bytes.Buffer{}, ef}, nil
}

// ExportSpan writes a SpanData in json format to stdout.
func (e *recorderExporter) ExportSpan(ctx context.Context, data *export.SpanData) {
	jsonSpan, err := json.Marshal(data)
	if err != nil {
		e.errorFunc(err)
		return
	}
	// ignore writer failures for now
	_, _ = e.outputWriter.Write(append(jsonSpan, byte('\n')))
}

type errorFunc func(error)
