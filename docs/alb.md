# Application Load Balancer

Trickster 2.0 provides an all-new Application Load Balancer that is easy to configure and provides unique features to aid with Scaling, High Availability and other applications. The ALB supports several balancing methodologies:

| Methodology | AKA | Benefits | Description |
|-----|-----|-----|----|
| Round Robin | rr | Scaling, HA | a basic, stateless round robin between healthy pool members |
| Time Series Merge | tsm | HA, Federation | uses scatter/gather to collect and merge data from multiple replica tsdb sources |
| First Response | fr | HA, Response Time | fans a request out to multiple origins, and returns the first response received |
| First Good Response | fgr | HA, Response Time | fans a request out to multiple origins, and returns the first response received with a status code < 400 |
| Newest Last-Modified | nlm | Freshness | fans a request out to multiple origins, and returns the response with the newest Last-Modified header |

## Integration with Backends

In Trickster, an ALB itself is a Backend, just like the pool members to which it routes.

The ALB works by applying a Methodology to select a Backend from a list of Pool Members, through which to route a request. Pool member names represent Backend Configs (known in Trickster 0.x and 1.x as Origin Configs) that can be pre-existing or newly defined.

All settings and functions configured for a Backend are applicable to traffic routed via an ALB - caching, rewriters, rules, tracing, TLS, etc.

## Methodologies Deep Dive

Each methodology has it's own use cases and pitfalls. Be sure to read about each one to understand how they might apply to your situation.

### Basic Round Robin

A basic round robin rotates through a pool of healthy backends used to service client requests. Each time a client request is made to Trickster, the round robiner will identify the next healthy backend in the rotation schedule and route the request to it.

The Trickster ALB is intended to support stateless workloads, and does not support Sticky Sessions or other advanced ALB capabilities.

#### Weighted Round Robin

Trickster supports Weighted Round Robin by permitting repeated pool member names in the same pool list. In this way, an operator can craft the desired proportion based on the number of times a given backend appears in the pool list. We've provided an example in the snippet below.

Trickster's round robiner cycles through the pool in the order it is defined in the Configuration file. Thus, when using Weighted Round Robin, it is recommended use a non-sorted, staggered ordering pattern in the pool list configuration, so as to prevent routing burts of consecutive requests to the same backend.

#### More About Our Round Robin Methodology

Trickster's ALB works by maintaining an atomic uint64 counter that increments each time a request is received by the ALB. The ALB then performs a modulo operation on the request's counter value, with the denominator being the count of healthy backends in the pool. The resulting value, ranging from `0` to `len(healthy_pool) -1` indicates the assigned backend based on the counter and current pool size.

#### Example Round Robin Configuration

```yaml
backends:

  # traditional Trickster backend configurations

  node01:
    provider: reverseproxycache # will cache responses to the default memory cache
    path_routing_disabled: true # disables frontend request routing via /node01 path
    origin_url: http://node01.example.com
    tls: # this backend might use mutual TLS Auth
      client_cert_path: ./cert.pem
      client_key_path: ./cert.key

  node02:
    provider: reverseproxy      # requests will be proxy-only with no caching
    path_routing_disabled: true # disables frontend request routing via /node02 path
    origin_url: http://node-02.example.com
    request_headers: # this backend might use basic auth headers
      Authoriziation: "basic jdoe:*****"

  # Trickster 2.0 ALB backend configuration, using other backends as pool members

  node-alb:
    provider: alb
    alb:
      mechanism: rr
      pool:
        - node01
        - node02
        # - node02 # if this node were uncommented, weighting would change to 33/67
        # add backends multiple times to establish a weighting protocol.
        # when weighting, use a cycling list, rather than a sorted list.
```

Here is the visual representation of this configuration:

<img src="./images/alb-rr.png" width="800">

### Time Series Merge

The recommended application for using the Time Series Merge methodology is as a High Availability solution. In this application, Trickster fans the client request out to multiple redundant tsdb endpoints and merges the responses back into a single document for the client. If any of the two endpoints are down, or have gaps in their respone (due to prior downtime), the Trickster cache along with the data from the healthy endpoints will ensure the client receives the most complete response possible. Instantaneous downtime of any Backend will result in a warning being injected in the client response.

Separate from an HA use case, it is possible to merge responses from different, non-redundant tsdb endpoints; for example, to aggregate responses from a solution running clusters in multiple regions, with their own in-region-only tsdb deployments. In this use case, it is recommended to [inject labels](./prometheus.md#injecting-labels) into the responses to protect against data collisions across series. Label injecion is demonstrated in the snippet below.

#### Providers Supporting Time Series Merge

Trickster currently suppports Time Series Merging for the following TSDB Providers:

| Provider Name |
|---|
| Prometheus |

We hope to support more TSDB's in the future and welcome any help!

#### Example TS Merge Configuration

```yaml
backends:

  # assume prom01a and prom01b are redundant and poll the same targets
  prom01a:
    provider: prometheus
    origin_url: http://prom01a.example.com:9090
    prometheus:
      labels:
        region: us-east-1

  prom01b:
    provider: prometheus
    origin_url: http://prom01b.example.com:9090
      labels:
        region: us-east-1

  # prom-alb-01 scatter/gathers prom01a and prom01a and merges responses for the caller
  prom-alb-01:
    provider: alb
    alb:
      methodology: tsmerge
      pool: 
        - prom01a
        - prom01b

  # assume prom02 and prom03 poll different targets but output the same metric names
  prom02:
    provider: prometheus
    origin_url: http://prom02.example.com:9090
      labels:
        region: us-east-2

  prom03:
    provider: prometheus
    origin_url: http://prom03.example.com:9090
      labels:
        region: us-west-1

  # prom-alb-02 scatter/gathers prom01, prom02 and prom03 and merges their responses
  # for the caller. since a unique region label was applied to non-redundant backends,
  # collisions should be avoided
  prom-alb-02:
    provider: alb
    alb:
      methodology: tsmerge
      pool: 
        - prom01a
        - prom01b
        - prom02
        - prom03
```

Here is the visual representation of a basic TS Merge configuration:

<img src="./images/alb-tsm.png" width="800">

### First Response

The **First Response** methodology fans a request out to all healthy pool members, and returns the first response received back to the client. All other fanned out responses are cached (if applicable) but otherwise discarded. If one backend in the fanout has already cached the requested object, and the other backends do not, the cached response will return to the caller while the other backends in the fanout will cache their responses as well for subsequent requests through the ALB.

This methodology works well when using Trickster as an HTTP object cache fronting multiple redundant origins, to ensure the fastest response possible is delivered to downstream clients - even if the HTTP Response Code indicates an error in the request or by the first backend to respond.

#### First Response Configuration Example

```yaml
backends:
  node01:
    provider: reverseproxycache
    origin_url: http://node01.example.com

  node02:
    provider: reverseproxycache
    origin_url: http://node-02.example.com

  node-alb-fr:
    provider: alb
    alb:
      mechanism: fr
      pool:
        - node01
        - node02
```

Here is the visual representation of this configuration:

<img src="./images/alb-fr.png" width="800">

### First Good Response

The **First Good Response** methodology acts just as First Response does, except that waits to return the first response with an HTTP Status Code < 400. If no fanned out response codes are in the acceptable range once all responses are returned (or the timeout has been reached), then the healthiest response, based on `min(all_responses_status_codes)`, is used.

This methodology is useful in applications such as live internet television, where an object may be written to Origin 1, and not yet written to redundant Origin 2, before users receive references to and begin requesting the object in a manifest. Trickster, when used as an ALB+Cache in this scenario, will poll both origins for the object and cache the positive responses for subsequent requests, while a negative cache configuration avoids a 404 storm on Origin 2 until the object can be written.

#### First Good Response Configuration Example

```yaml

negative-caches:
  default: # by default, backends use the 'default' negative cache
    "404": 500 # cache 404 responses for 500ms

backends:
  node01:
    provider: reverseproxycache
    origin_url: http://node01.example.com

  node02:
    provider: reverseproxycache
    origin_url: http://node-02.example.com

  node-alb-fgr:
    provider: alb
    alb:
      mechanism: fgr
      pool:
        - node01
        - node02
```

Here is the visual representation of this configuration:

<img src="./images/alb-fgr.png" width="800">

### Newest Last Modified

The **Newest Last Modified** methodology is focused on providing the user with the _newest_ representation of the response, rather than responding as quickly as possible. It will fan the client request out to all backends, and wait for all responses to come back (or the ALB timeout to be reached) before determining which response is returned to the user.

If at least one fanout response has a `Last-Modified` header, then any response not containing the header is discarded. The remaning responses are sorted based on their Last Modified header value, and the newest value determines which response is chosen.

This methodology is useful in applications where an object residing at the same path on multiple origins is updated frequently, such as a DASH or HLS manifest for a live video broadcast. When using Trickster as an ALB+Cache in this scenario, it will poll both backends for the object, and ensure the newest version between them is used as the client response.

Note that with NLM, the response to the user is only as fast as the slowest backend to respond.

#### First Good Response Configuration Example

```yaml
backends:
  node01:
    provider: reverseproxycache
    origin_url: http://node01.example.com

  node02:
    provider: reverseproxycache
    origin_url: http://node-02.example.com

  node-alb-nlm:
    provider: alb
    alb:
      mechanism: nlm
      pool:
        - node01
        - node02
```

Here is the visual representation of this configuration:

<img src="./images/alb-nlm.png" width="800">

