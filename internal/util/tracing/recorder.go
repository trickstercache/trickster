package tracing

import (
	"bytes"
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel/api/trace"
	export "go.opentelemetry.io/otel/sdk/export/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setRecorderExporter(ef errorFunc, opts *ExporterOptions) (trace.Tracer, func(), *recorderExporter, error) {
	f := func() {}
	exporter, _ := newRecorder(ef)

	tp, err := sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.ProbabilitySampler(opts.sampleRate)}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		return tp.Tracer(""), f, nil, err
	}
	return tp.Tracer(""), f, exporter, nil
}

// recorderExporter is an implementation of trace.Exporter that writes spans to a buffer, and saves the span data for later inspection.
type recorderExporter struct {
	buf   *bytes.Buffer
	spans []*export.SpanData
}

func (r *recorderExporter) Read(buf []byte) (int, error) {

	return r.buf.Read(buf)
}
func (r *recorderExporter) Write(buf []byte) (int, error) {

	return r.buf.Write(buf)
}

// newRecorder returns a newly instantiated recorder
func newRecorder(ef errorFunc) (*recorderExporter, error) {
	buf := new(bytes.Buffer)
	return &recorderExporter{buf, nil}, nil
}

// ExportSpan writes a SpanData in json format to buffer.
func (r *recorderExporter) ExportSpan(ctx context.Context, data *export.SpanData) {
	jsonSpan, _ := json.Marshal(data) // data is typed and nil doesn't error
	r.spans = append(r.spans, data)
	// ignore writer failures for now
	r.Write(append(jsonSpan, byte('\n')))
}

type errorFunc func(error)
