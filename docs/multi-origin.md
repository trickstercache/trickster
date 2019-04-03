# Using Multiple-Origins with a single Trickster instance

Trickster supports proxying to multiple origins by examining the inbound request and using a multiplexer to direct the proxied request to the correct upstream origin, in the same way that web servers support virtual hosting. Multi-origin does _not_ equate to High Availability support; Trickster does not offer any kind of redundancy features. Using Multiple Origins simply means that a single Trickster instance can accelerate any number of unrelated upstream origins instead of requiring a Trickster instance per-origin.

There are 2 ways to configure multi-origin support.

* HTTP Pathing
* DNS Aliasing

## Basic Usage

To utilize Multiple Origins, you must craft a Trickster configuration file to be read when Trickster starts up - multi-origin is not supported with environment variables or command line arguments. The [example.conf](../cmd/trickster/conf/example.conf) provides good documentation and commented sections demonstrating multi-origin. The config file should be placed in `/etc/trickster/trickster.conf` unless you specify a different path when starting Trickster with the `-config` command line argument.

Each origin that your Trickster instance supports must be explicitly enumerated in the configuration file. Trickster does not support open proxying.

Each origin is identified by an Origin Name, provided in the configuration section header for the origin ([origins.NAAME]). For path-based routing configurations, the Origin Name can be simple words. For DNS Aliasing, the Origin Name must match an FQDN that resolves to your Trickster instance.

In all cases, if Trickster cannot identify a valid origin by the client-provided Origin Name, it will proxy the request to the default origin.

### Path-based Routing Configurations

In this mode, Trickster will use a single FQDN but still map to multiple upstream origins. This is the simplest setup and requires the least amount of work. The client will indicate which origin is desired in URL Path for the request.

Example Path-based Multi-Origin Configuration:
```
[origins]

    # default origin
    [origins.default]
        origin_url = 'http://prometheus.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400

    # "foo" origin
    [origins.foo]
        origin_url = 'http://influxdb-foo.example.com:9090'
        origin_type = 'influxdb'
        cache_name = 'default'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400

    # "bar" origin
    [origins.bar]
        origin_url = 'http://prometheus-bar.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400
```

#### Using HTTP Path as the Multi-Origin Indicator

The client prefixes the Trickster request path with the Origin Name.

This is the recommended method for integrating multi-origin support into Grafana.

Example Client Request URLs:
* To Request from Origin `foo`: http://trickster.example.com:9090/foo/query?query=xxx

* To Request from Origin `bar`: http://trickster.example.com:9090/bar/query?query=xxx

* To Request from Origin `default` (Method 1, no Origin Name): http://trickster.example.com:9090/query?query=xxx

* To Request from Origin `default` (Method 2, with Origin Name): http://trickster.example.com:9090/default/query?query=xxx

* Configuring Grafana to request from origin `foo` via Trickster:

<img src="./images/grafana-path-origin.png" width=610 />

### DNS Alias Configuration

In this mode, multiple DNS records point to a single Trickster instance. The FQDN used by the client to reach Trickster represents the Origin Name. Therefore, the entire FQDN must be part of the configuration section header. In this mode, the URL Path is _not_ considered during Origin Selection.

Example DNS-based Origin Configuration:
```
[origins]

    # default origin
    [origins.default]
        origin_url = 'http://prometheus.example.com:9090'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400

    # "foo" origin
    [origins.trickster-foo.example.com]
        origin_url = 'http://prometheus-foo.example.com:9090'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400

    # "bar" origin
    [origins.trickster-bar.example.com]
        origin_url = 'http://prometheus-bar.example.com:9090'
        api_path = '/api/v1'
        default_step = 300
        ignore_no_cache_header = false
        max_value_age_secs = 86400

```

Example Client Request URLs:
*  To Request from Origin `foo`: http://trickster-foo.example.com:9090/query?query=xxx

*  To Request from Origin `bar`: http://trickster-bar.example.com:9090/query?query=xxx

*  To Request from Origin `default`: http://trickster.example.com:9090/query?query=xxx
