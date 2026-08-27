# TLS Support

Trickster supports TLS on both the frontend server and backend clients.

## Basics

To enable the TLS server, specify the `tls_port`, and optionally, the `tls_address` in one of the `listeners` of your config file. For example:

```yaml
listeners:
  # default is built-in and is the default listener for backends
  # it uses these values by default:
  default:
    port: 8480
    tls_port: 8483
    # listen on all interfaces
    tls_address: ''
```

Note, Trickster will only start listening on a TLS port if at least one origin mapped to the named listener has a valid certificate and key configured.

Each origin section of a Trickster config file can be augmented with the optional `tls` section to modify TLS behavior for front-end and back-end requests. For example:

```yaml
backends:
  example: # example backend
    tls:   # TLS settings for example backend
      # frontend configs
      full_chain_cert_path: '/path/to/my/cert.pem'
      private_key_path: '/path/to/my/key.pem'
      # backend configs
      insecure_skip_verify: true
      certificate_authority_paths: [ '/path/to/ca1.pem', '/path/to/ca2.pem' ]
      client_cert_path: '/path/to/client/cert.pem'
      client_key_path: '/path/to/client/key.pem'
```

## Server Configs - used when responding to clients

Each backend contributes up to 1 certificate and key pair, as configured in the TLS section of the backend config (demonstrated above). A listener serves the certificates of all backends mapped to it, selecting per-handshake by SNI (see [Certificate Selection (SNI)](#certificate-selection-sni) below), so a single TLS listener can serve many hostnames.

If the path to any configured Certificate or Key file is unreachable or unparsable, Trickster will exit upon startup with an error providing reasonable context.

You may use the same TLS certificate and key for multiple backends, depending upon how your Trickster configurations are laid out. Any certificates configured by Trickster must match the hostname header of the inbound http request (exactly, or by wildcard interpolation), or clients will likely reject the certificate for security issues.

## Certificate Selection (SNI)

When a listener has multiple certificates, Trickster selects the certificate for each TLS handshake in this order:

1. Exact match of the client's SNI hostname against a certificate's Subject Alternative Names
2. Wildcard match (e.g. a `*.example.com` certificate for `foo.example.com`)
3. A linear compatibility scan (covers clients that send no SNI, and IP SANs)
4. The listener's first certificate, if nothing else matches

The exact and wildcard lookups are index-based, so per-handshake selection cost is independent of the number of certificates a listener serves.

## Automatic Certificate Rotation Detection

Trickster automatically detects when a serving certificate is renewed in place on disk — same file paths, config untouched, as performed by tools like certbot or cert-manager — and hot-swaps the renewed certificate into the live listener. This is entirely independent of configuration reloads: no manual reload, `auto_reload_interval`, or config change is required.

- Detection is hybrid: filesystem events (via fsnotify) trigger a near-immediate check where the platform and filesystem support them, and a timer-based poll runs regardless — both as the fallback for deployments where filesystem events don't work (e.g. some network and FUSE filesystems) and as a self-healing backstop for missed events. If event watching is unavailable, detection silently degrades to poll-only.
- Every check compares file contents (not modification times or event payloads), so detection works across platforms and through the atomic symlink swaps used by Kubernetes Secret and projected volumes.
- The certificate, key (and any associated CA bundle) files are watched and validated as one unit: if a poll observes a mid-rotation partial state (e.g. the cert file updated before the key file), the mismatched pair is never served; the last-good pair keeps serving and the change is retried on the next poll.
- Read errors and invalid content never disable detection: the watcher keeps retrying, logs a warning after sustained failures, and the last-good certificate keeps serving. Deleting the files is treated as a persistent failure, not as certificate removal.
- Startup behavior is unchanged: an unreadable or invalid configured certificate is still a fatal startup error. Only post-startup source failures are tolerated.

Rotation detection is on by default and is configured per listener. `tls_watch_interval` sets the fallback/backstop poll cadence (default 30s); filesystem events, where available, apply rotations within moments regardless of the interval. Setting the interval to 0 disables rotation detection entirely (events included):

```yaml
listeners:
  default:
    tls_port: 8483
    # backstop poll interval for the cert/key files of mapped backends
    # (default 30s); filesystem events accelerate detection where supported.
    # set to 0 to disable automatic rotation detection.
    tls_watch_interval: 30s
```

Note: automatic rotation detection currently applies to HTTP(S) listeners. Native-protocol listeners (e.g. `mysql`) pick up rotated certificates on config reload.

## Hot Swap and No-Close Semantics

Certificate swaps — whether from a config reload or automatic rotation detection — never restart, drain, or rebind the listener:

- The certificate is consulted only at handshake time, so established connections (including keep-alive connections and in-flight requests) are untouched by a swap; they continue on the certificate they were handshaken with until they close naturally.
- Only new handshakes see the new certificate.

## Certificate Inventory (mgmt)

The mgmt listener exposes a read-only, per-listener certificate inventory at `/trickster/certificates` (configurable via `mgmt.certificates_handler_path`). Each entry reports the certificate's id, source kind (`file`, `memory` or `config`), common name, subject alternative names, validity window and last-load time. The inventory never includes key material.

## Observability

Certificate rotation and inventory are covered by the following metrics: `trickster_tls_certificate_expiration_time_seconds`, `trickster_tls_certificate_last_load_time_seconds`, `trickster_tls_certificate_swaps_total`, `trickster_tls_certificate_validation_failures_total`, `trickster_tls_watcher_errors_total`, and `trickster_tls_certificate_store_size`. See [metrics.md](./metrics.md) and the example alerting rules in [examples/alerting](../examples/alerting/tls-certificates-alerts.yaml) for cert-expiry, sustained-validation-failure and watcher-staleness alerts.

## Client Configs - used when proxying to an origin

Each backend's TLS configuration can also configure the https client used for making requests against the origin as demonstrated above.

`insecure_skip_verify` will instruct the http client to ignore hostname verification issues with the upstream origin's certificate, and process the request anyway. This is analogous to `-k | --insecure` in curl.

`certificate_authority_paths` will provide the http client with a list of certificate authorities (used in addition to any OS-provided root CA's) to use when determining the trust of an upstream origin's TLS certificate. In all cases, the Root CA's installed to the operating system on which Trickster is running are used for trust by the client.

To us Mutual Authentication with an upstream origin server, configure Trickster with Client Certificates using `client_cert_path` and `client_key_path` parameters, as shown above. You will likely need to also configure a custom CA in `certificate_authority_paths` to represent your certificate signer, unless it has been added to the underlying Operating System's CA list.
