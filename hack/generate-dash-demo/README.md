# generate-dash-demo

Drives realistic traffic through a demo Trickster, so the
[Grafana dashboards](../../deploy/grafana) have data to show, for demos and
screenshots.

```bash
make generate-dash-demo
```

The target starts the [developer environment](../../docs/developer/environment)
(`make developer-start`), then runs a demo Trickster with
[trickster-generate-dash-demo.yml](./trickster-generate-dash-demo.yml) and the
generator. Ctrl-C stops both. The demo Trickster's ports (18980–18990) are
clear of `make serve-dev`'s, so both can run at once. Its log is in
`/tmp/trickster-dash-demo/trickster.log`.

Prometheus scrapes the demo Trickster as job `trickster-demo`, instance
`trickster-demo-01.us-east-1`. In Grafana, open the Trickster folder at
<http://127.0.0.1:3000> and pick that job.

## Traffic

- **Dashboards.** Every 5 seconds, a round of time series queries runs in
  series: Prometheus, TSM (global evaluation and merge), PostgreSQL over the
  wire protocol, ClickHouse, Druid and Graphite. Most rounds look at the last
  hour, and some look further back.
- **Browsers.** Simulated visitors come and go. Each loads pages, assets and
  API calls from a catalog of a few hundred objects of varied sizes and
  lifetimes, sometimes plays video segments in byte ranges, revalidates now and
  then, and occasionally follows a broken link or hits a failing call.
- **MoQ.** The generator publishes four live channels into the ingest relay over
  native QUIC. Viewers come and go over WebTransport through the edge relay,
  and some fetch the last few seconds before they subscribe.

Browser and viewer counts follow a slow wave, a two-hour swell with a
13-minute ripple, so graphs rise and fall as real traffic does.

## Flags

Pass flags with `GEN_DASH_DEMO_ARGS`, for example
`make generate-dash-demo GEN_DASH_DEMO_ARGS='-browsers 32 -viewers 16'`.

| Flag | Default | Description |
|---|---|---|
| `-browsers` | `16` | simulated browsers at the peak of the wave |
| `-viewers` | `10` | MoQ viewers at the peak of the wave |
| `-interval` | `5s` | the pause between rounds of time series queries |
| `-report` | `1m` | how often to log a traffic summary |
