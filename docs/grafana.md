# Grafana Origin Support

Trickster can use Grafana as an origin while continuing to accelerate supported time series data sources. In this mode, clients connect to Trickster instead of Grafana directly. Trickster transparently proxies the Grafana UI and API, and dynamically dispatches recognized data source proxy requests through its existing time series backend providers.

## Configuration

Configure a backend with `provider: grafana` and set `origin_url` to the Grafana base URL:

```yaml
backends:
  default:
    provider: grafana
    origin_url: http://grafana:3000
```

See [`simple.grafana.yaml`](../examples/conf/simple.grafana.yaml) for a runnable configuration example.

## Data Source Discovery

At startup, Trickster makes a best-effort request to Grafana's `/api/datasources` endpoint. When it receives an unrecognized data source proxy path later, it looks up that data source by numeric ID or UID and remembers the result for subsequent requests.

Trickster recognizes both Grafana data source proxy path forms:

- `/api/datasources/proxy/:id/*`
- `/api/datasources/proxy/uid/:uid/*`

Discovery uses the incoming `Authorization`, `Cookie`, `X-Grafana-Org-Id`, `X-WEBAUTH-USER`, and `X-JWT-Assertion` headers. This lets lazy discovery use the same Grafana identity as the proxied request. For startup discovery or service-to-service use, a header can be configured on the root path:

```yaml
backends:
  default:
    provider: grafana
    origin_url: http://grafana:3000
    paths:
      - path: /
        match_type: prefix
        methods: [ '*' ]
        handler: grafana
        request_headers:
          Authorization: Bearer ${GRAFANA_SERVICE_TOKEN}
```

## Acceleration Boundary

Grafana-proxied data sources using the following types are dispatched through the corresponding Trickster provider:

| Grafana data source type | Trickster provider |
| --- | --- |
| `prometheus` | Prometheus |
| `influxdb` | InfluxDB |
| `vertamedia-clickhouse-datasource` | ClickHouse |

Only data sources configured with Grafana's `proxy` access mode are accelerated. Unsupported data source types, browser-access data sources, and all other Grafana UI and API traffic are transparently proxied without caching.

Grafana's `/api/ds/query` endpoint is also transparently proxied. That endpoint can contain multiple queries and data sources in a Grafana data-frame envelope, so it cannot safely reuse a single Trickster time series provider without a dedicated request and response modeler.

Cached data source responses are separated by the listed Grafana identity headers to prevent data from being shared across users or organizations.

If Grafana uses a custom authentication or tenant header, add that header to the root path's `cache_key_headers`. Trickster will then forward it during data source discovery and include it when separating cached responses.
