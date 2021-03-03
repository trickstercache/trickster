module github.com/tricksterproxy/trickster

go 1.16

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/alicebob/gopher-json v0.0.0-20200520072559-a9ecdc9d1d3a // indirect
	github.com/alicebob/miniredis v2.5.0+incompatible
	github.com/dgraph-io/badger v1.6.2
	github.com/dgraph-io/ristretto v0.0.3 // indirect
	github.com/go-kit/kit v0.10.0
	github.com/go-redis/redis v6.15.9+incompatible
	github.com/go-stack/stack v1.8.0
	github.com/golang/snappy v0.0.3
	github.com/gomodule/redigo v1.8.4 // indirect
	github.com/gorilla/handlers v1.5.1
	github.com/gorilla/mux v1.8.0
	github.com/influxdata/influxdb v1.8.4
	github.com/prometheus/client_golang v1.9.0
	github.com/tinylib/msgp v1.1.5
	github.com/tricksterproxy/mockster v1.1.1
	github.com/yuin/gopher-lua v0.0.0-20200816102855-ee81675732da // indirect
	go.etcd.io/bbolt v1.3.5
	go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace v0.17.0
	go.opentelemetry.io/otel v0.17.0
	go.opentelemetry.io/otel/exporters/stdout v0.17.0
	go.opentelemetry.io/otel/exporters/trace/jaeger v0.16.0 // thrift incompat leaves at 0.16.0 for now 1/Mar/21
	go.opentelemetry.io/otel/exporters/trace/zipkin v0.17.0
	go.opentelemetry.io/otel/sdk v0.17.0
	go.opentelemetry.io/otel/trace v0.17.0
	golang.org/x/mod v0.4.1 // indirect
	golang.org/x/net v0.0.0-20210226172049-e18ecbb05110
	golang.org/x/sys v0.0.0-20210301091718-77cc2087c03b // indirect
	golang.org/x/tools v0.1.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	gopkg.in/yaml.v2 v2.4.0
)
