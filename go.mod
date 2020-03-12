module github.com/Comcast/trickster

replace gotest.tools => github.com/gotestyourself/gotest.tools v2.2.0+incompatible

require (
	github.com/AndreasBriese/bbloom v0.0.0-20190825152654-46b345b51c96 // indirect
	github.com/BurntSushi/toml v0.3.1
	github.com/alicebob/gopher-json v0.0.0-20180125190556-5a6b3ba71ee6 // indirect
	github.com/alicebob/miniredis v2.5.0+incompatible
	github.com/coreos/bbolt v1.3.3
	github.com/dgraph-io/badger v1.6.0
	github.com/dgryski/go-farm v0.0.0-20200201041132-a6ae2369ad13 // indirect
	github.com/go-kit/kit v0.9.0
	github.com/go-logfmt/logfmt v0.5.0 // indirect
	github.com/go-redis/redis v6.15.6+incompatible
	github.com/go-stack/stack v1.8.0
	github.com/golang/snappy v0.0.1
	github.com/gomodule/redigo v2.0.0+incompatible // indirect
	github.com/gorilla/handlers v1.4.2
	github.com/gorilla/mux v1.7.3
	github.com/influxdata/influxdb v1.7.9
	github.com/philhofer/fwd v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.3.0
	github.com/prometheus/common v0.8.0
	github.com/stretchr/testify v1.4.0
	github.com/tinylib/msgp v1.1.1
	github.com/yuin/gopher-lua v0.0.0-20190514113301-1cd887cd7036 // indirect
	go.opentelemetry.io/otel v0.2.0
	go.opentelemetry.io/otel/exporter/trace/jaeger v0.2.0
	golang.org/x/net v0.0.0-20200114155413-6afb5195e5aa
	golang.org/x/sys v0.0.0-20200116001909-b77594299b42 // indirect
	google.golang.org/grpc v1.24.0
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
)

go 1.14
