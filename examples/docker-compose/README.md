# Docker Compose Example

This composition will spin up service containers for Prometheus, Grafana, Jaeger, Zipkin, Redis, Trickster and Mockster that together demonstrate several basic end-to-end configurations for running Trickster in your environment with different cache and tracing provider options. Note - if you have any of these services running locally already, you may run into port conflicts and need to temporarily spin down the conflicting processes.

## Getting Starting

First, make sure you have Docker and Docker Compose installed. This varies from system-to-system, and this document assumes you have this handled already. Then, clone the Trickster Github project and change your working directory to `./examples/docker-compose`.

To run the demo, from the demo directory, run `docker-compose up -d`

You can then interact with each of the services on their exposed ports (as defined in [Compose file](./docker-compose.yml)), or by running `docker logs $container_name`, `docker attach $container_name`, etc.

Once the composition is running, a great place to start exploring is Grafana, which will be running at <http://127.0.0.1:3000/> if the composition came up without issue. Grafana is pre-configured with datasources and sample dashboard that are ready-to-use for the demo.

Also, take a look at Jaeger UI, available at <http://127.0.0.1:16686>, which provides visualization of traces shipped by Trickster and Grafana. The more you use trickster-based data sources in Grafana, the more traces you will see in Jaeger. This composition runs the Jaeger All-in-One container, and Trickster ships some traces to the Agent, and others directly to the Collector, so as to demonstrate both capabilities. The Trickster config determines which upstream origin ships which traces to where.

Speaking of, definitely review the various files in the `docker-compose-data` folder, which is full of configurations and other bootstrap data. This might be useful for configuring and using Trickster (or any of these other fantastic projects) in your own deployments. It might be fun to add, remove or change some of the trickster configurations in [./docker-compose-data/trickster-config/trickster.yaml](./docker-compose-data/trickster-config/trickster.yaml) and then `docker exec docker-compose_trickster_1 kill -1 1` into the Trickster container to apply the changes, or restart the environment altogether with `docker-compose restart`. Just be sure to make a backup of the original config first, so you don't have to download it again later.

## Example Datasources

The `sim-*` datasources generate on-the-fly simulation data for any possible timerange, so you can immediately use them after starting up the environment. Note, however, that the simulated data is not representative of reality in any way.

The non-sim Prometheus container that backs the `prom-*` datasources polls the newly-running environment to generate metrics that will then populate the dashboard. Since the Prometheus container only collects and stores metrics while the environment is running, you'll need to wait a minute or two for those datasources to show any data on the dashoard in real-time.

## Getting Real Dashboard Data

Using datasources backed by the real Prometheus and Trickster (the `prom-trickster-*` datasources), rather than the simulator, to explore the dashboard is more desirable for the demo. It better conveys the shape and nature of the Trickster-specific metrics that might be unfamiliar. However, since there is no historical data in the demo composition, that creates an upfront barrier.

Keeping the dashboard open and auto-refreshing against any `trickster`-labeled datasource will help to generate real metrics in Trickster, such as request rates, cache hit rates, etc. Prometheus will collect and store those metrics, and the Grafana dashboard will query and render those metrics. So by keeping the demo dashboard open and refreshing, you are helping to generate the very metrics that the dashboard presents, making the demo much more visually useful while being very meta.

In addition to generating metrics, using the `trickster`-labeled datasources generates traces that are viewable in Jaeger UI, as described above.

## Stopping the Demo and Cleaning Up

To stop and remove the demo, run `docker-compose down` from this directory.
