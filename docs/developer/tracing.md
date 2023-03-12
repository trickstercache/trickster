# Tracing support

Trickster has minimal support for OpenTelemetry. See <https://github.com/trickstercache/trickster/issues/36>

## Config
TODO

## Developer Testing

### Manual with Jaeger

1. Start jaeger

```
docker run -d --name jaeger \                                                                                                                                                                                                                         ()
  -e COLLECTOR_ZIPKIN_HTTP_PORT=9411 \
  -p 5775:5775/udp \
  -p 6831:6831/udp \
  -p 6832:6832/udp \
  -p 5778:5778 \
  -p 16686:16686 \
  -p 14268:14268 \
  -p 9411:9411 \
  jaegertracing/all-in-one:1.
```

2. Star Promsim

From the Trickster root run

```
go run cmd/promsim/main.go
```

3. Start Trickster with tracing test config

From the Trickster root run
```
make build -o trickster && ./bin/trickster -config testdata/test.tracing.conf --log-level debug
```

4. Query

```
curl -i 'localhost:8080/test/api/v1/query_range?query=my_test_query{random_label="57",series_count="1"}&start=2&end=4&step=1'
```

5. Visit Jaeger UI at http://localhost:16686/search

### Testing Utilities

`tracing.Recorder` is a trace exporter for capturing exported trace spans for inspection later.

`tracing.TestHTTPClient` is an http client with a mock `http.RoundTripper` that calls a test handler for faking http calls.

`SetupTestingTracer` uses the above type to setup a mock tracer whose spans can be validated.

Usage together (from `recorder_test.go`:
```go
flush, ctx, recorder, tr := SetupTestingTracer(t, RecorderTracer, 1.0, TestContextValues)

err := tr.WithSpan(ctx, "Testing trace with span",
        func(ctx context.Context) error {
                var (
                        err error
                )
                req, _ := http.NewRequest("GET", "https://example.com/test", nil)

                ctx, req = httptrace.W3C(ctx, req)
                httptrace.Inject(ctx, req)
                _, err = TestHTTPClient().Do(req)
                if err != nil {
                        return err
                }

                SpanFromContext(ctx).SetStatus(codes.OK)

                return nil

        })

assert.NoError(t, err, "failed to inject span")
flush()
m := make(map[string]string)
for _, kv := range TestEvents {
        m[string(kv.Key)] = kv.Value.Emit()

}

for _, span := range recorder.spans {
        for _, msg := range span.MessageEvents {
                for _, attr := range msg.Attributes {
                        key := string(attr.Key)
                        wantV, ok := m[key]
                        assert.True(t, ok, "kv not in known good map")
                        v := attr.Value.Emit()
                        assert.Equal(t, wantV, v, "Span Message attribute value incorrect")

                }
        }

}
```
