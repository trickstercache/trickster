# Docker Compose Example

This composition will spin up service containers for Prometheus, Grafana, Jaeger, Redis, Trickster and devorigin (a small origin service built from this repo) that together demonstrate several basic end-to-end configurations for running Trickster in your environment with different cache and tracing provider options. Note - if you have any of these services running locally already, you may run into port conflicts and need to temporarily spin down the conflicting processes.

## Getting Starting

First, make sure you have Docker and Docker Compose installed. This varies from system-to-system, and this document assumes you have this handled already. Then, clone the Trickster Github project and change your working directory to `./examples/docker-compose`.

To run the demo, from the demo directory, run `docker-compose up -d`

The first start takes a minute or two longer than later ones: it generates the synthetic trips seed data, builds devorigin from the repo, and loads 15 days of trips history into Prometheus before Prometheus starts. All of this runs offline in `golang` and `prom/prometheus` containers.

You can then interact with each of the services on their exposed ports (as defined in [Compose file](./docker-compose.yml)), or by running `docker logs $container_name`, `docker attach $container_name`, etc.

Once the composition is running, a great place to start exploring is Grafana, which will be running at <http://127.0.0.1:3000/> if the composition came up without issue. Grafana is pre-configured with datasources and dashboards that are ready-to-use for the demo. The `Trickster` folder has Trickster's [published dashboards](../../deploy/grafana), which chart Trickster's own metrics; start with **Trickster / Overview**.

Also, take a look at Jaeger UI, available at <http://127.0.0.1:16686>, which provides visualization of traces shipped by Trickster and Grafana. The more you use trickster-based data sources in Grafana, the more traces you will see in Jaeger. This composition runs Jaeger v2 in its all-in-one configuration. Trickster sends traces to Jaeger's OTLP/HTTP endpoint on port 4318, while Grafana uses Jaeger's compact Thrift receiver on UDP port 6831. The Trickster config determines which upstream origins produce traces.

Speaking of, definitely review the various files in the `docker-compose-data` folder, which is full of configurations and other bootstrap data. This might be useful for configuring and using Trickster (or any of these other fantastic projects) in your own deployments. It might be fun to add, remove or change some of the trickster configurations in [./docker-compose-data/trickster-config/trickster.yaml](./docker-compose-data/trickster-config/trickster.yaml) and then `docker exec docker-compose_trickster_1 kill -1 1` into the Trickster container to apply the changes, or restart the environment altogether with `docker-compose restart`. Just be sure to make a backup of the original config first, so you don't have to download it again later.

## Example Datasources

The `Trips (Prometheus)` dashboard has data as soon as the environment is up. Prometheus starts with the last 15 days of trips in the fictional city of Emberwick already loaded, and then scrapes new trips from devorigin as time passes. Use its datasource selector to compare querying Prometheus directly against querying it through the various Trickster caches. The seed data uses the `small` profile; for ten times as many trips, stop the demo with `docker-compose stop`, then start it with `SEED_PROFILE=default docker-compose up -d`. Prometheus is only reseeded while it is stopped.

The `sim-*` datasources are backed by devorigin's Prometheus API simulator, which generates on-the-fly simulation data for any query and any possible timerange, so you can immediately use them after starting up the environment. Note, however, that the simulated data is not representative of reality in any way.

The non-sim Prometheus container that backs the `prom-*` datasources also scrapes Trickster's own metrics. Since it only collects and stores metrics while the environment is running, you'll need to wait a minute or two for the `Trickster` dashboards to show any data.

## Getting Real Dashboard Data

The `Trickster` dashboards chart what Trickster is doing, so they come alive once Trickster is handling traffic. The quickest way to produce some is to keep the `Trips (Prometheus)` dashboard open and auto-refreshing against a `prom-trickster-*` datasource, which sends every query through Trickster.

The `Trickster` dashboards have their own Data source selector, which defaults to `prom-direct`. Point it at a `prom-trickster-*` datasource instead, and the dashboards query Trickster's metrics through Trickster itself. By keeping them open and refreshing, you are helping to generate the very metrics that they present, which is very meta. Dashboards for features the demo config doesn't use, such as load balancing or TLS, stay empty.

In addition to generating metrics, using the `trickster`-labeled datasources generates traces that are viewable in Jaeger UI, as described above.

## Stopping the Demo and Cleaning Up

To stop and remove the demo, run `docker-compose down` from this directory.
