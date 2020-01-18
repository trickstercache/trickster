# Tracing support

Trickster has minimal support for OpenTelemetry. See https://github.com/Comcast/trickster/issues/36

## Known issues
1. All traces are happening twice. Unsure why.

## Config
TODO

## Developer Testing

Jaeger

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
make build -o trickster && ./OPATH/trickster -config testdata/test.tracing.conf --log-level debug
```

4. Query

```
curl -i 'localhost:8080/test/api/v1/query_range?query=my_test_query{random_label="57",series_count="1"}&start=2&end=4&step=1'
```

5. Visit Jaeger UI at http://localhost:16686/search

