# TLS Support

Trickster supports TLS on both the frontend server and backend clients.

## Basics

To enable the TLS server, you must specify the `tls_listen_port`, and optionally, the `tls_listen_address` in the `frontend` section of your config file. For example:

```yaml
frontend:
  listen_port: 8480
  tls_listen_port: 8483
```

Note, Trickster will only start listening on the TLS port if at least one origin has a valid certificate and key configured.

Each origin section of a Trickster config file can be augmented with the optional `tls` section to modify TLS behavior for front-end and back-end requests. For example:

```yaml
backends:
  example: # example backend
    tls:   # TLS settings for example backend
      # server configs
      full_chain_cert_path: '/path/to/my/cert.pem'
      private_key_path: '/path/to/my/key.pem'
      # back-end configs
      insecure_skip_verify: true
      certificate_authority_paths: [ '/path/to/ca1.pem', '/path/to/ca2.pem' ]
      client_cert_path: '/path/to/client/cert.pem'
      client_key_path: '/path/to/client/key.pem'
```

## Server Configs - used when responding to clients

Each backend can handle encryption with exactly 1 certificate and key pair, as configured in the TLS section of the backend config (demonstrated above).

If the path to any configured Certificate or Key file is unreachable or unparsable, Trickster will exit upon startup with an error providing reasonable context.

You may use the same TLS certificate and key for multiple backends, depending upon how your Trickster configurations are laid out. Any certificates configured by Trickster must match the hostname header of the inbound http request (exactly, or by wildcard interpolation), or clients will likely reject the certificate for security issues.

## Client Configs - used when proxying to an origin

Each backend's TLS configuration can also configure the https client used for making requests against the origin as demonstrated above.

`insecure_skip_verify` will instruct the http client to ignore hostname verification issues with the upstream origin's certificate, and process the request anyway. This is analogous to `-k | --insecure` in curl.

`certificate_authority_paths` will provide the http client with a list of certificate authorities (used in addition to any OS-provided root CA's) to use when determining the trust of an upstream origin's TLS certificate. In all cases, the Root CA's installed to the operating system on which Trickster is running are used for trust by the client.

To us Mutual Authentication with an upstream origin server, configure Trickster with Client Certificates using `client_cert_path` and `client_key_path` parameters, as shown above. You will likely need to also configure a custom CA in `certificate_authority_paths` to represent your certificate signer, unless it has been added to the underlying Operating System's CA list.
