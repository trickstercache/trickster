# AWS Integration

Trickster uses AWS credentials and SigV4 request signing in two places, and
both are configured the same way:

- **Signing outbound requests to an origin** — the `sigv4` block on a
  backend, below.
- **Autodiscovery of AWS resources** — the [`aws` discovery
  provider](./alb-autodiscovery.md), which reads an AWS API to keep an ALB
  pool current.

## Credentials

Leaving the credential fields empty selects the standard AWS credential
chain, in the order the AWS SDK resolves it:

1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
   `AWS_SESSION_TOKEN`)
2. The shared credentials and config files (`~/.aws/credentials`,
   `~/.aws/config`), honoring `profile`
3. Web-identity tokens — this is how **EKS IAM Roles for Service Accounts
   (IRSA)** and **EKS Pod Identity** work
4. AWS SSO
5. The EC2 instance metadata service (IMDSv2)

Prefer the chain over static keys wherever the platform provides one: on
EKS use IRSA or Pod Identity, on EC2 use an instance profile. Static keys
are supported for environments that have nothing else.

### Credential Lifecycle

Credentials are resolved **lazily**, on the first signed request. Trickster
therefore starts even when the instance metadata service is briefly
unreachable. Concurrent first requests share one resolution.

- **Failures are retried.** A failed resolution is reused for 5 seconds and
  then tried again, so a momentary metadata or STS failure does not
  permanently disable signing, and an outage costs one blocked resolution
  per 5 seconds rather than one per request.
- **Expiring credentials refresh early.** Temporary credentials (IRSA, Pod
  Identity, SSO, instance profiles, `role_arn`) are refreshed between 2.5
  and 5 minutes before they expire, rather than after, so requests are not
  signed with credentials that lapse in flight.
- **Rejected credentials are renewed once.** When the origin answers 403
  with `ExpiredToken`, `ExpiredTokenException`, `InvalidClientTokenId`,
  `UnrecognizedClientException` or `InvalidSignatureException`, Trickster
  renews the credentials and resends the request once:
  - Credentials that expire (IRSA, Pod Identity, SSO, instance profiles,
    `role_arn`) are discarded and resolved again. Concurrent rejections of
    the same credentials cause a single refresh.
  - Credentials read from the shared credentials or config file, including
    temporary keys written there by a refresher, carry no expiry, so the
    files are read again instead, at most once every 5 seconds. A
    rewritten file therefore takes effect without a reload.
  - `access_key` and `secret_key` set in the `sigv4` block, and keys from
    environment variables, cannot change while Trickster runs, so they are
    never retried.
- **One cache per backend.** A backend's proxy and health check requests
  share one set of credentials.
- **Clock skew.** SigV4 signatures carry a timestamp, and AWS rejects
  requests more than 5 minutes off its clock with
  `InvalidSignatureException`. Keep the host clock synchronized.

Signing failures and credential retries are counted in
`trickster_proxy_sigv4_events_total` (see [metrics](./metrics.md)) and
logged as warnings, at most once a minute per backend and kind.

## Region

`region` may be set explicitly. When it is not, it is resolved from
`AWS_REGION` / `AWS_DEFAULT_REGION`, the shared config file, or instance
metadata. If none of those yields one, requests fail with an error naming
every source that was tried.

## The `sigv4` Backend Block

`sigv4` signs Trickster's outbound requests to a backend's origin.

```yaml
backends:
  amp:
    provider: prometheus
    origin_url: https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-abc123
    sigv4:
      region: us-east-1
      # credentials omitted: use the chain (IRSA, instance profile, ...)
      # access_key: AKIA...
      # secret_key: ...        # redacted in config dumps and the health page
      # profile: production
      # role_arn: arn:aws:iam::123456789012:role/TricksterRead
      # service: aps           # default; see below
```

| option | meaning |
| ----- | ----- |
| `region` | region to sign for; resolved from the environment when unset |
| `access_key`, `secret_key` | a static credential pair — both or neither |
| `profile` | a profile in the shared config file |
| `role_arn` | a role to assume with whatever the chain resolves first |
| `service` | the AWS service to sign for; defaults to `aps` |

**The `service` default is `aps`** — Amazon Managed Service for Prometheus.
That is deliberate: earlier releases could sign for nothing else, so every
existing config keeps working unchanged. Set `service` to sign for a
different AWS service (`es` for OpenSearch, and so on).

`access_key` and `secret_key` must be provided together. A config supplying
only one fails at startup rather than silently falling through to the
chain and authenticating as a different principal.

`secret_key` is redacted wherever configuration is emitted — the config
dump, the management API, logs, and error messages.

### Amazon Managed Service for Prometheus

The common case. Point a `prometheus` backend at the workspace's query URL
and add a `sigv4` block; Trickster caches and accelerates AMP queries as it
does any other Prometheus origin.

```yaml
backends:
  amp:
    provider: prometheus
    origin_url: https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-abc123
    sigv4:
      region: us-east-1
```

The IAM principal needs `aps:QueryMetrics`, `aps:GetSeries`,
`aps:GetLabels`, and `aps:GetMetricMetadata` on the workspace.

Adding `prometheus.flavor: amp` restricts the backend to AMP's read APIs
(`query`, `query_range`, `series`, `labels`, `label/<name>/values`,
`metadata`, `rules` and `alerts`). Every other path, including
`/api/v1/remote_write`, is answered by Trickster with an error instead of
being signed and forwarded. The flavor also adds the `sigv4` block if it is
missing.

```yaml
backends:
  amp:
    provider: prometheus
    origin_url: https://aps-workspaces.us-east-1.amazonaws.com/workspaces/ws-abc123
    prometheus:
      flavor: amp
    sigv4:
      region: us-east-1
```

### Amazon CloudWatch PromQL

CloudWatch serves PromQL over a Prometheus-compatible API at
`https://monitoring.<region>.amazonaws.com`, covering metrics ingested
through OTLP and enriched AWS vended metrics. Queries are billed by the
samples they scan, so a dashboard that refreshes repeatedly pays again for
every sample it has already seen. Trickster's delta cache fetches only the
new part of each refresh, and request collapsing merges concurrent viewers
into one upstream query.

```yaml
backends:
  cloudwatch:
    provider: prometheus
    prometheus:
      flavor: cloudwatch
    sigv4:
      region: us-east-1
      # credentials omitted: use the chain (IRSA, instance profile, ...)
```

Grafana's Prometheus data source points at Trickster as it would at any
Prometheus server; Trickster does the signing.

`flavor: cloudwatch` applies CloudWatch's routes, limits and defaults:

- **Signing:** the `sigv4` block is added if it is missing, and its
  `service` defaults to `monitoring`.
- **Origin:** `origin_url` defaults to
  `https://monitoring.<sigv4.region>.amazonaws.com`. When the region comes
  from the environment instead of `sigv4.region`, set `origin_url`
  explicitly. An `origin_url` and `sigv4.region` naming different regions
  fail at startup.
- **Routes:** only `query`, `query_range`, `series`, `labels` and
  `label/<name>/values` are served. Every other path is answered by
  Trickster with an error instead of being signed and forwarded, so a
  client cannot use Trickster's credentials to call other CloudWatch APIs.
- **Cache keys:** `limit`, which caps the series a query returns, is part
  of each cache key.
- **Truncated results:** CloudWatch returns at most 500 series per query,
  or `limit` when smaller. A result cut short at that cap comes back with a
  warning and HTTP 200. Such a result is a sample of the series, and a
  later delta fetch can return a different sample, so Trickster never
  caches or merges one. It evicts any cached entry for the query, proxies
  the query to CloudWatch whole, and proxies that query directly for the
  backend's `timeseries_ttl`. Each occurrence is counted in
  `trickster_proxy_truncated_responses_total`. Narrow the query, for
  example with an aggregation, to bring it back under the cap and into the
  cache.
- **Volatile window:** `volatile_window` defaults to `2m`, so the most
  recent two minutes are refetched while late data arrives. Any explicit
  `volatile_window` or `volatile_window_points` takes precedence.
- **Health check:** the default probe is `query=vector(1)`, which reads no
  metric and so scans no samples.

The IAM principal needs `cloudwatch:GetMetricData` and
`cloudwatch:ListMetrics`; `series`, `labels` and label values need only
`cloudwatch:ListMetrics`.

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["cloudwatch:GetMetricData", "cloudwatch:ListMetrics"],
    "Resource": "*"
  }]
}
```

#### CloudWatch Limits

These CloudWatch limits apply per account and are not changed by Trickster.
Delta fetches and request collapsing reduce how often a dashboard reaches
them.

| limit | value |
| ----- | ----- |
| query requests per second | 300 |
| discovery requests (`series`, `labels`, label values) per second | 10 |
| concurrent queries, and concurrent discovery requests | 30 each |
| range per query, including range selectors and lookback | 7 days |
| execution timeout | 20 seconds |

Exceeding a rate or size limit returns HTTP 422, and exceeding concurrency
returns HTTP 429; Trickster does not cache either. Every selector must name
a metric: `{__name__=~"..."}` alone is rejected.

#### Making AWS Vended Metrics Queryable

Metrics ingested through OTLP are queryable as they arrive. Classic AWS
vended metrics, such as DynamoDB's, become queryable through PromQL only
after both enrichment settings are turned on for the account and region:

```bash
aws cloudwatch start-otel-enrichment --region us-east-1
aws observabilityadmin start-telemetry-enrichment --region us-east-1
```

Enriched metrics carry their resource attributes as UTF-8 labels, such as
`@aws.region` and `@resource.cloud.resource_id`.

#### Cost Example

CloudWatch bills PromQL queries at $0.01 per million samples scanned. The
figures below are an illustration, not a measurement; check the savings
against your own bill.

Take a dashboard of 20 panels, each summing a rate over 50 series of
one-minute data, showing the last 6 hours and refreshing every 60 seconds:

- **Without Trickster:** each refresh scans about 6 hours plus a 5-minute
  lookback per series: 50 × 365 ≈ 18,000 samples per panel, or 365,000
  per refresh. That is about 526 million samples a day, or about $5.26 a
  day ($158 per 30 days) for one screen left open.
- **With Trickster:** after the first load, each refresh fetches only the
  new step and the 2-minute volatile window, plus the lookback: about 8
  samples per series, 400 per panel, or 8,000 per refresh. That is about
  11.5 million samples a day, or about $0.12 a day.

Every additional viewer of the same dashboard costs that much again
without Trickster, and close to nothing with it, since concurrent requests
for the same query are collapsed and later ones are served from the cache.

#### Live Tests

The CloudWatch tests in `pkg/backends/prometheus/cloudwatch_live_test.go`
run against a real account and are skipped by default. They need a metric
that is visible to PromQL in the account:

```bash
TRICKSTER_AWS_TEST=1 TRICKSTER_AWS_PROFILE=default TRICKSTER_CW_METRIC=ConsumedReadCapacityUnits go test ./pkg/backends/prometheus/ -run Live -v -count=1
```

### Notes

- SigV4 signs a hash of the request body, so Trickster buffers a request
  body in order to sign it. This applies only to backends with a `sigv4`
  block.
- SigV4 is not supported for the ClickHouse **native** protocol, which is
  not HTTP; configuring both fails at startup.

## IAM for Autodiscovery

The `aws` discovery provider needs read-only permission for whichever
`aws.service` it is configured with.

| `aws.service` | required IAM actions |
| ----- | ----- |
| `ec2` | `ec2:DescribeInstances` |
| `ecs` | `ecs:ListTasks`, `ecs:DescribeTasks` |

A minimal policy for `service: ec2`:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "ec2:DescribeInstances",
    "Resource": "*"
  }]
}
```

`ec2:DescribeInstances` does not support resource-level permissions, so the
resource must be `*`; narrow the scope with a condition key if your
environment requires it. `ecs:ListTasks` and `ecs:DescribeTasks` can be
scoped to a cluster with the `ecs:cluster` condition key. Trickster only
ever reads.
