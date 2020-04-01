module github.com/tricksterproxy/trickster

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/Comcast/trickster v1.0.2
	github.com/alicebob/miniredis v2.5.0+incompatible
	github.com/coreos/bbolt v1.3.3
	github.com/dgraph-io/badger v1.6.0
	github.com/dgryski/go-farm v0.0.0-20200201041132-a6ae2369ad13 // indirect
	github.com/go-kit/kit v0.9.0
	github.com/go-redis/redis v6.15.6+incompatible
	github.com/go-stack/stack v1.8.0
	github.com/golang/snappy v0.0.1
	github.com/gorilla/handlers v1.4.2
	github.com/gorilla/mux v1.7.4
	github.com/influxdata/influxdb v1.7.9
	github.com/prometheus/client_golang v1.5.0
	github.com/prometheus/common v0.9.1
	github.com/stretchr/testify v1.5.1
	github.com/tinylib/msgp v1.1.1
	github.com/tricksterproxy/mockster v0.0.0-20200228034438-cc033fc7cf65
	go.opentelemetry.io/otel v0.2.0
	go.opentelemetry.io/otel/exporter/trace/jaeger v0.2.0
	golang.org/x/net v0.0.0-20200114155413-6afb5195e5aa
	google.golang.org/grpc v1.24.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
	gopkg.in/yaml.v2 v2.2.7 // indirect
)

go 1.14
