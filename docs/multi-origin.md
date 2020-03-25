# Using Multiple-Origins with a single Trickster instance

Trickster supports proxying to multiple origins by examining the inbound request and using a multiplexer to direct the proxied request to the correct upstream origin, in the same way that web servers support virtual hosting. Multi-origin does _not_ equate to High Availability support; Trickster does not offer any kind of redundancy features. Using Multiple Origins simply means that a single Trickster instance can accelerate any number of unrelated upstream origins instead of requiring a Trickster instance per-origin.

There are 2 ways to configure multi-origin support.

* HTTP Pathing
* DNS Aliasing

## Basic Usage

To utilize Multiple Origins, you must craft a Trickster configuration file to be read when Trickster starts up - multi-origin is not supported with simply environment variables or command line arguments. The [example.conf](../cmd/trickster/conf/example.conf) provides good documentation and commented sections demonstrating multi-origin. The config file should be placed in `/etc/trickster/trickster.conf` unless you specify a different path when starting Trickster with the `-config` command line argument.

Each origin that your Trickster instance supports must be explicitly enumerated in the configuration file. Trickster does not support open proxying.

Each origin is identified by an Origin Name, provided in the configuration section header for the origin ([origins.NAME]). For path-based routing configurations, the Origin Name can be simple words. For DNS Aliasing, the Origin Name must match an FQDN that resolves to your Trickster instance. Also for DNS Aliasing, enclose the FQDN in quotes in the origin config section header (e.g., `[origins.'db.example.com']`).

### Default Origin

Whether proxying to one or more upstreams, Trickster has the concept of a "default" origin, which means it does not require a specific DNS hostname in the request, or a specific URL path, in order to proxy the request to a known origin. When a default origin is configured, if the inbound request does not match any mapped origins by path or FQDN, the request will automatically be mapped to the default origin. You are probably familiar with this behavior from when you first tried out Trickster with the using command line arguments.

Here's an example: if you have Trickster configured with an origin named `foo` that proxies to `http://foo/` and is configured as the default origin, then requesting `http://trickster/image.jpg` will initiate a proxy request to `http://foo/image.jpg`, without requiring the path be prefixed with `/foo`. But requesting to `http://trickster/foo/image.jpg` would also work.

The default origin can be configured by setting `is_default = true` for the origin you have elected to make the default.  Having a default origin is optional. In a single-origin configuration, Trickster will automatically set the sole origin as `is_default = true` unless you explicly set `is_default = false` in the configuration file. If you have multiple origins, and don't wish to have a default origin, you can just omit the value for all origins. If you set `is_default = true` for more than one origin, Trickster will exit with a fatal error on startup.

### Path-based Routing Configurations

In this mode, Trickster will use a single FQDN but still map to multiple upstream origins. This is the simplest setup and requires the least amount of work. The client will indicate which origin is desired in URL Path for the request.

Example Path-based Multi-Origin Configuration:
```
[origins]

    # origin1 origin
    [origins.origin1]
        origin_url = 'http://prometheus.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'
        is_default = true

    # "foo" origin
    [origins.foo]
        origin_url = 'http://influxdb-foo.example.com:9090'
        origin_type = 'influxdb'
        cache_name = 'default'

    # "bar" origin
    [origins.bar]
        origin_url = 'http://prometheus-bar.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'
```

#### Using HTTP Path as the Multi-Origin Indicator

The client prefixes the Trickster request path with the Origin Name.

This is the recommended method for integrating multi-origin support into Grafana.

Example Client Request URLs:

* To Request from Origin `foo`: <http://trickster.example.com:8480/foo/query?query=xxx>

* To Request from Origin `bar`: <http://trickster.example.com:8480/bar/query?query=xxx>

* To Request from Origin `origin1` as default: <http://trickster.example.com:8480/query?query=xxx>

* To Request from Origin `origin1` (Method 2, with Origin Name): <http://trickster.example.com:8480/origin1/query?query=xxx>

* Configuring Grafana to request from origin `foo` via Trickster:

<img src="./images/grafana-path-origin.png" width=610 />

### DNS Alias Configuration

In this mode, multiple DNS records point to a single Trickster instance. The FQDN used by the client to reach Trickster is mapped to specific origin configurations using the `hosts` list. In this mode, the URL Path is _not_ considered during Origin Selection.

Example DNS-based Origin Configuration:

```toml
[origins]

    # origin1 origin
    [origins.origin1]
        hosts = [ '1.example.com', '2.example.com' ] # users can route to this origin via these FQDNs, or via `/origin1`
        origin_url = 'http://prometheus.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'
        is_default = true

    # "foo" origin
    [origins.foo]
        hosts = [ 'trickster-foo.example.com' ] # users can route to this origin via these FQDNs, or via `/foo`
        origin_url = 'http://prometheus-foo.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'

    # "bar" origin
    [origins.bar]
        hosts = [ 'trickster-bar.example.com' ] # users can route to this origin via these FQDNs, or via `/bar`
        origin_url = 'http://prometheus-bar.example.com:9090'
        origin_type = 'prometheus'
        cache_name = 'default'

```

Example Client Request URLs:

* To Request from Origin `foo`: <http://trickster-foo.example.com:8480/query?query=xxx>

* To Request from Origin `bar`: <http://trickster-bar.example.com:8480/query?query=xxx>

* To Request from Origin `origin1` as default: <http://trickster.example.com:8480/query?query=xxx> 

* To Request from Origin `origin1` (Method 2, via FQDN): <http://origin1.example.com:8480/query?query=xxx>

Note: It is currently possible to specify the same FQDN in multiple origin configurations. You should not do this (obviously). A future enhancement will cause Trickster to exit fatally upon detection at startup.
