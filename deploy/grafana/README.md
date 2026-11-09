# Trickster Grafana Dashboards

This directory holds the official Grafana dashboards for monitoring Trickster
itself, from the metrics it exposes on its `/metrics` endpoint (port 8481 by
default). See [docs/metrics.md](../../docs/metrics.md) for the metrics.

The dashboards form one collection meant for a single "Trickster" folder. Each
one is tagged `trickster` and has a **Trickster dashboards** link menu at the
top that opens the others with the same time range and selections. Start at
**Trickster / Overview**, which has a tile per area that links to its
dashboard.

| Dashboard | File | What it covers |
|---|---|---|
| Trickster / Overview | [trickster-overview.json](./dashboards/trickster-overview.json) | Fleet health at a glance: instances, versions, uptime, reloads, request rate, errors, latency, cache hit ratio, plus one health tile per area |
| Trickster / HTTP Traffic | [trickster-http.json](./dashboards/trickster-http.json) | Front end requests, status classes, latency, egress, client connections, backend health checks, retries, mirroring, SRV resolution and SigV4 |
| Trickster / Caching | [trickster-caching.json](./dashboards/trickster-caching.json) | Cache engine outcomes by cache status, object cache operations, latency, capacity, evictions and errors, and the static file server's cache |
| Trickster / Time Series Acceleration | [trickster-timeseries.json](./dashboards/trickster-timeseries.json) | Data points served from cache, delta cache behavior (partial buckets, step alignment, retention), and query analysis for SQL, Druid, VictoriaMetrics and Graphite backends |
| Trickster / Load Balancing | [trickster-alb.json](./dashboards/trickster-alb.json) | ALB pool and member health, fanout, sticky sessions and autodiscovery |
| Trickster / Time Series Merge (TSM) | [trickster-tsm.json](./dashboards/trickster-tsm.json) | TSM merge replica groups, global evaluation and its raw series cache |
| Trickster / L4 Streams | [trickster-l4.json](./dashboards/trickster-l4.json) | `tcp`, `tls` and `udp` stream listeners and their pool members |
| Trickster / SQL Wire Protocols | [trickster-sql-wire.json](./dashboards/trickster-sql-wire.json) | MySQL and PostgreSQL wire protocol listeners, routing, and native SQL cache outcomes |
| Trickster / MoQ Relay | [trickster-moq.json](./dashboards/trickster-moq.json) | Media over QUIC sessions, subscriptions, objects, live edge and fetches |
| Trickster / Access Control & Rate Limiting | [trickster-access.json](./dashboards/trickster-access.json) | IP ACLs, geo ACLs and locators, and rate limiters |
| Trickster / TLS & ACME | [trickster-tls.json](./dashboards/trickster-tls.json) | Certificate expiry and rotation, and ACME orders, renewals and challenges |
| Trickster / Kubernetes Controller | [trickster-kubernetes.json](./dashboards/trickster-kubernetes.json) | The in-process Gateway API / Ingress controller: sync health, reconciles, watch events and routes |
| Trickster / Runtime & Lifecycle | [trickster-runtime.json](./dashboards/trickster-runtime.json) | Build info, config reloads, recovered panics, dropped log lines, and Go runtime and process resources |

Dashboards for features you don't use (L4 streams, MoQ, access control, TLS,
Kubernetes, TSM) show no data; you can skip importing them.

## Requirements

- Grafana 10.4 or later.
- A Prometheus-compatible data source (Prometheus, Mimir, Thanos,
  VictoriaMetrics, Grafana Cloud...) that scrapes each Trickster's metrics
  endpoint.

Nothing is hardcoded to a particular setup. Every dashboard has these variables:

- **Data source**, which defaults to your default Prometheus data source.
- **Job** and **Instance**, which are read from `trickster_build_info`. Any
  scrape job name works, including the job a Kubernetes ServiceMonitor or pod
  annotation produces.

Some dashboards add area-specific variables, such as Backend, Cache or ALB.

## Installing

### Import in the Grafana UI

1. In Grafana, open **Dashboards → New → Import**.
2. Upload a JSON file from [dashboards/](./dashboards), or paste its contents.
3. Pick a folder (we suggest one named `Trickster`) and click **Import**.

Repeat for each dashboard you want. Keep the dashboard UIDs as they are,
because the cross-dashboard links use them.

### Provision from files

Copy the [dashboards/](./dashboards) directory to your Grafana host, for
example to `/var/lib/grafana/dashboards/trickster`. Then add the provider in
[provisioning/trickster-dashboards.yaml](./provisioning/trickster-dashboards.yaml)
to Grafana's `provisioning/dashboards` directory. It loads them into a
`Trickster` folder.

```bash
git clone --depth 1 https://github.com/trickstercache/trickster.git
sudo mkdir -p /var/lib/grafana/dashboards/trickster
sudo cp trickster/deploy/grafana/dashboards/*.json /var/lib/grafana/dashboards/trickster/
sudo cp trickster/deploy/grafana/provisioning/trickster-dashboards.yaml /etc/grafana/provisioning/dashboards/
```

### Kubernetes (Grafana dashboard sidecar)

If your Grafana runs the dashboard sidecar, as the Grafana Helm chart and
kube-prometheus-stack can, [kustomization.yaml](./kustomization.yaml) generates
one ConfigMap per dashboard. Each is labeled `grafana_dashboard: "1"` and
annotated `grafana_folder: Trickster`.

```bash
kubectl apply --server-side -n monitoring -k 'https://github.com/trickstercache/trickster//deploy/grafana?ref=main'
```

To place them in a `Trickster` folder, set the sidecar's folder annotation
(`sidecar.dashboards.folderAnnotation: grafana_folder` and
`sidecar.dashboards.provider.foldersFromFilesStructure: true` in the Grafana
chart values). Otherwise they land in the sidecar's default folder.

### Grafana HTTP API

```bash
GRAFANA=http://localhost:3000
for f in deploy/grafana/dashboards/*.json; do
  jq '{dashboard: ., overwrite: true, folderUid: "trickster"}' "$f" |
    curl -sf -u admin:admin -H 'Content-Type: application/json' \
      -X POST "$GRAFANA/api/dashboards/db" -d @- >/dev/null && echo "imported $f"
done
```

Create a folder with UID `trickster` first, or leave out `folderUid` to import
into the General folder.

### Try them out

The [Docker Compose demo](../../examples/docker-compose) and the
[developer environment](../../docs/developer/environment) both provision these
dashboards into a `Trickster` folder. In the developer environment,
`make generate-dash-demo` runs a demo Trickster with realistic traffic across
most dashboards; see [hack/generate-dash-demo](../../hack/generate-dash-demo).

## Making changes

These files are the source of truth. To change a dashboard, edit it in the
developer environment (`make developer-start` and `make serve-dev`, then open
<http://127.0.0.1:3000>). Then export it with **Export → Export as JSON**, with
**Export the dashboard to use in another instance** turned off, and replace the
file here. Before committing:

- Keep the `uid`, and set `"id": null`.
- Reset the `datasource`, `job` and `instance` variables' `current` selections,
  so the dashboard doesn't carry your environment's values.
- Panels and queries must use the `${datasource}` variable, never a data source
  UID, and every Trickster selector must filter on `job=~"$job", instance=~"$instance"`.
- Run `make lint`, which spell-checks these files.

## Publishing to grafana.com

Each grafana.com listing holds exactly one dashboard, so every file here is its
own listing under the Trickster organization. When a dashboard changes, upload
its file as a new revision of its listing: on the listing page, use
**Revisions → Upload revision**.
