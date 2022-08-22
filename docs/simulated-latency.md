# Simulated Latency

Trickster supports simulating latency on a per-backend basis, and can simulate both consistent and random durations of latency. Simulated latency is introduced in the frontend part of the proxy, and thus works with any backend provider.

In `rule`, `alb` and other backend providers, where a request may transit multiple backend routes, only the Simulated Latency configs associated with the request entrypoint (first route) will be processed, and not any subsequent routes the request is sent through.

## Consistent Latency Duration

In the Backend configuration, add a `latency_min_ms` value > 0, and the configured amount of latency will be introduced for each incoming request.

## Random Latency

 To simulate random latency, set `latency_max_ms` to a value > `latency_min_ms`, which may be 0 for random latency. Trickster will introduce a random amount of latency between (inclusive) the provided min and max values.

## Response Header

When simulated latency is applied to a request, an `x-simulated-latency` header will be included in the corresponding response indicating the duration of the latency applied in milliseconds. The format of the latency header is as follows:

```bash
GET /api/v1/query?query=up HTTP/1.1
Accept: */*
...

HTTP/1.1 200 OK
X-Simulated-Latency: 300ms
X-Trickster-Result: engine=DeltaProxyCache; status=hit; ffstatus=hit
Date: Mon, 06 Sep 2021 04:07:20 GMT
...
```

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

  backend2:
    origin_url: https://www.trickstercache.org
    provider: reverseproxy
    #
    # introduce latency of 300 milliseconds on each request
    latency_min_ms: 300
```
