# Prometheus Support

Trickster fully supports accelerating Prometheus, which we consider our First Class backend provider. They work great together, so you should give it a try!

Most configuration options that affect Prometheus reside in the main Backend config, since they generally apply to all TSDB providers alike.

We offer one custom configuration for Prometheus, which is the ability to inject labels, on a per-backend basis to the Prometheus response before it is returned to the caller.

## Injecting Labels

Here is the basic configuration for adding labels:

```yaml
backends:
  prom-1a:
    provider: prometheus
    origin_url: http://prometheus-us-east-1a:9090
    prometheus:
      labels:
        datacenter: us-east-1a

  prom-1b:
    provider: prometheus
    origin_url: http://prometheus-us-east-1b:9090
    prometheus:
      labels:
        datacenter: us-east-1b
```
