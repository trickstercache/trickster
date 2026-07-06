# Setting up a Local Developer Environment

## Prerequisites

* Docker is installed and running, and the `docker` command is available.
* Golang 1.26 is installed
* Make and other tools are installed

## Running

A Docker Compose file is available that starts up and seeds the containers needed
for your developer environment. This includes TSDBs like Prometheus, and Dashboard
apps like Grafana.

From the root of the repo, run `make developer-start` to start the environment.

Next, you must run Trickster from your local repo, by running `make serve-dev`.
This runs `cmd/trickster/main.go` with a config file from the developer environment.

You can combine these make actions `make developer-start serve-dev` if you want.

Once you have the Docker Compose running, and Trickster running locally, visit
the Grafana Dashboard at <http://127.0.0.1:3000/d/uAJ8w1wZz/trickster-status>.
The Kibana frontend is available at <http://127.0.0.1:5601> and is configured
to use Trickster's `es1` Elasticsearch backend at
`http://host.docker.internal:8480/es1`.

The data in this dashboard is polled by Prometheus from your local Trickster
dev instance. So the longer you keep this dashboard up and refreshing, the more
you can test out Trickster acceleration features. You can change the Data Source
selector to go between various Trickster configs, or bypass Trickster altogether
for verification purposes.

Elasticsearch is seeded on startup with the `trickster-dev-logs` index. The seed
data includes recent and older `@timestamp` values so developers can verify
Elasticsearch date histogram caching through Trickster.

You can stop the developer environment by running `make developer-stop`. To
delete the developer environment, run `make developer-delete` which will destroy
all data.
