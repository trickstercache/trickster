# Where to Place Trickster

Depending upon the size of your existing or planned deployment, there are several placement configurations available. These designs are suggestions based on common usage, and you may find alternative or hybrid placement configurations that make the most sense for your situation, based on the activity of your Dashboard and TSDB instance(s).

## Single "Everything"

<img src="./images/deploy-single-everything.png" width="600"/>

Single "Everything" is the most common placement model. In this configuration, you have one dashboard endpoint, one Trickster endpoint and one TSDB endpoint. Behind each endpoint, you may have a single instance or a cluster. Each component is only aware of the other component's endpoint exposure and not the underlying configuration. This configuration represents a one-for-one-for-one deployment of your Dashboard, TSDB, and Trickster endpoints.

## Multi-Origin

<img src="./images/deploy-multi-origin.png" width="600"/>

In a Multi-Origin placement, you have one dashboard endpoint, one Trickster endpoint, and multiple TSDB endpoints. Trickster is aware of each TSDB endpoint and treats them as unique origins to which it proxies and caches data independently of each other. Trickster selects the origin based on Host Header or URL Path from the client request.

This is a good configuration to use when you have a single dashboard that displays data about multiple redundant clusters (each with its own TSDB), or when you have a single dashboard representing information about many different kinds of systems. For example, if you operate a "Dashboard as a Service" solution under which many teams use your Dashboard system by designing their own dashboard screens and bringing their own databases, a single Trickster endpoint can be used to accelerate dashboards for all of your customers.

You will need to configure each Trickster-to-TSDB mapping separately in your dashboard application as a separately named TSDB data source. Refer to the [multi-origin](./multi-origin.md) documentation for configuring multi-origin support in Trickster and Grafana.

In this configuration, be aware that the default 'memory' cache may be underpowered depending on the number of customers, as well as the size and number of queries that need to be cached by each customer. Refer to the [caches](./caches.md) document to select and configure the caching layers as needed to meet your specific situation.

## Multi-Trickster

<img src="./images/deploy-multi-trickster.png" width="600"/>

In a Multi-Trickster configuration, you have one dashboard endpoint, multiple Trickster endpoints, and multiple TSDB endpoints, with each Trickster Endpoint having a one-to-one mapping to a TSDB Endpoint as a pair. This is a good design if Multi-Origin is not performant enough for the amount of activity associated with your solution (e.g., you need more Tricksters). If the Dashboard system owner is different from the TSDB system owner, either party could own and operate the Trickster instance.
