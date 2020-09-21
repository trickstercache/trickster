# Trickster 1.1

## What's Improved

1.1 continues to improve the Trickster project, with a ton of new features, bug fixes, and optimizations. Here's the quick rundown of what's new and improved:

- Our GitHub project is relocated from `Comcast/trickster` to `tricksterproxy/trickster`
- Our Docker Hub organization name is changed from `tricksterio` to `tricksterproxy`
- Helm charts are relocated to `tricksterproxy/helm-charts` and published at <https://helm.tricksterproxy.io>
- All project references and package imports updated per the project relocation
- All project packages are moved from `./internal` to `./pkg` to facilitate importation by other projects
- Trickster releases are now published using fully-automated, secure GitHub Actions
- New [trickster-demo](../deploy/trickster-demo) Docker Compose reference environment for anyone to easily see Trickster in action
- Added Windows support; win64 binaries are now included in Release
- We now use a single, all-platforms release tarball, complete with `bin` `docs` and `conf` directories
- Trickster-specific default listener ports: `8480` (http), `8481` (metrics), `8483` (tls), `8484` (reload)
- In-process config reloading via HUP or optional http listener endpoint
- Added `-validate-config` command line flag
- Customizable pprof debugging server configurations
- Updated to OpenTelemetry 0.6.0, streamlined Tracing configs, added Zipkin exporter support
- Updated Named Locks package to support RWMutex for concurrent cached object reads
- New Rules Engine for custom request handling and rewriting
- HTTP 2.0 Support
- systemd service file (`trickster.service`) is relocated from `./cmd/trickster/conf/` to `./deploy/systemd/`
- `rangesim` package has been rebranded as `mockster`, and moved to [its own project](https://github.com/tricksterproxy/mockster), with its own docker image using port `8482`
- Fully support acceleration of HTTP POST requests to Prometheus and InfluxDB endpoints
- Updated dependencies to Go 1.14.3, Alpine 3.12, InfluxDB 1.8.0

## Installing

You can build the 1.1 binary from the `main` branch, download binaries from the Releases page, or use the `tricksterproxy/trickster:1.1` Docker image tag in containerized environments. Helm Charts version `1.5.0` is the chart release associated with Trickster v1.1.

## Breaking Changes from 1.0

### Port Changes

If you rely on default settings in your deployment, rather than setting explicit values, be prepared to make adjustments to accommodate Trickster's new default ports. We encourage you to adjust your Trickster deployments to explicitly use the new default ports.

### Distributed Tracing Configuration

The `[tracing]` section of the Trickster TOML config specification has changed slightly, and is incompatible with a v1.0 config. If you use the tracing feature, be sure to check the [example.conf](../cmd/trickster/conf/example.conf) and adjust yours accordingly.

## Known Issues

### Zipkin

- Zipkin implementation currently works with the OpenZipkin Collector, but not Jaeger Collector's Zipkin-compatible endpoints
