# Simulated Latency

Trickster supports simulating latency on a per-backend basis, and can simulate both consistent and random durations of latency. Simulated latency is introduced in the frontend part of the proxy, and thus works with any backend provider.

In `rule`, `alb` and other backend providers, where a request may transit multiple backend routes, only the Simulated Latency configs associated with the request entrypoint (first route) will be procssed, and not any subsequent routes the request is sent through.

## Consistent Latency Duration

In the Backend configuration, add a `latency_min_ms` value > 0, and the configured amount of latency will be introduced for each incoming request.

## Random Latency

 To simulate random latency, set `latency_max_ms` to a value > `latency_min_ms`, which may be 0 for random latency. Trickster will introduce a random amount of latency between (inclusive) the provided values.

## Example Config

```yaml
frontend:
  listen_port: 8480

backends:
  default:
    origin_url: https://www.example.com
    provider: reverseproxy
    #
    # introduce random latency between 50 and 150 milliseconds on each request
    latency_min_ms: 50
    latency_max_ms: 150
```