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

func setRecorderTracer(ef errorFunc, sampleRate float64) (func(), *recorderExporter, error) {
	f := func() {}
	exporter, err := newRecorder(ef)
	if err != nil {
		return f, nil, err
	}

	tp, err := sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.ProbabilitySampler(sampleRate)}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		return f, nil, err
	}
	global.SetTraceProvider(tp)
	return f, exporter, nil
}

// recorderExporter is an implementation of trace.Exporter that writes spans to a buffer, and saves the span data for later inspection.
type recorderExporter struct {
	io.Reader
	outputWriter io.Writer
	spans        []*export.SpanData
	errorFunc    errorFunc
}

// newRecorder returns a newly instantiated recorder
func newRecorder(ef errorFunc) (*recorderExporter, error) {
	buf := new(bytes.Buffer)

	return &recorderExporter{buf, buf, nil, ef}, nil
}

// ExportSpan writes a SpanData in json format to buffer.
func (e *recorderExporter) ExportSpan(ctx context.Context, data *export.SpanData) {
	jsonSpan, err := json.Marshal(data)
	if err != nil {
		e.errorFunc(err)
	}
	e.spans = append(e.spans, data)
	// ignore writer failures for now
	e.outputWriter.Write(append(jsonSpan, byte('\n')))
}

type errorFunc func(error)
