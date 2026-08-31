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

Credentials are resolved **lazily**, on the first signed request, and only a
successful resolution is cached. Trickster therefore starts even when the
instance metadata service is briefly unreachable, and a momentary metadata
failure does not permanently disable signing.

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
environment requires it. Trickster only ever reads.
