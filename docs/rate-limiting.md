# Rate Limiting

Trickster can count requests, connections, or datagrams against named limiters and refuse the ones that exceed a limit. This page is the configuration skeleton. The enforcement behavior is described with the fields below as it lands.

Limiters are defined once under `rate_limiters` and attached by `rate_limiter_name`.

```yaml
rate_limiters:
  per-client:
    keys: [client_ip]     # empty means one bucket for every event
    limit: 600            # events per window
    window: 1m            # 1s to 1h; default 1m
    algorithm: sliding_window

listeners:
  default:
    rate_limiter_name: per-client

backends:
  api:
    rate_limiter_name: per-client
    paths:
      - path: /healthz
        rate_limiter_name: none   # clears the backend limiter; the listener limiter remains
```

A path's `rate_limiter_name` replaces the backend's. `none` is valid on a path only. The listener's limiter is not replaced by either of them.

See [example.full.yaml](../examples/conf/example.full.yaml) for the full field list.
