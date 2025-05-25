# Authenticator

Trickster 2.0 provides a new Authenticator capability that allows you to protect Backends with an Authentication layer.

Authenticator resources are defined globally by name, and then mapped into any Backend and/or Path configuration as needed. Authenticators can be loaded from `htpasswd` or `csv` files, or directly in the Trickster config file.

When embedding in the config file, you can include credentials in plaintext or pre-encrypted with bcrypt; but you must specify `users_format: bcrypt` when credentials are pre-encrypted. See `example_auth_3` in the blob below.

If you are loading Users into the same Authenticator from both a CSV and the embedded manifest, you must use the same format of credential (plaintext or bcrypt) for both. If the credentials are already encrypted, you must provide the encryption format in `users_format: bcrypt`. If no `users_format` value is supplied, credential are assumed to be provided in plaintext. Trickster will internally bcrypt any plaintext credential upon loading into the Authenticator. However, any plaintext credentials provided will persist in any Environment Variables or files from which they are sourced via configuration.

Authenticators work with all Backend provider types. Requests are handled by their respective Authenticators before all other Handlers (e.g., Caches, Rules, Request Rewrites, ALB Routes, etc.).

If a request is routed via a Trickster ALB or Rule Backend through to multiple other Backends - each having different Authenticator configurations - the authentication behavior is currently undefined. In an upcoming Beta release, we will define this use case to either use the Authenticator config (if set) of the very first Backend that handled the request; or to use the first defined Authenticator regardless of how deep into the Backend chain it is.

By default, when an Authenticator handles and successfully authenticates a request, the request's Auth credentials (e.g., Authorization Header for Basic Auth) are stripped before the request is cloned for any necessary proxying. Setting `proxy_preserve: true` will preserve these headers instead of stripping them.

## Path Protection

When you map an Authenticator to a Backend, all Paths defined for that Backend are protected by the Authenticator. However, you can override the Authenticator on a per-Path basis by including the `authenticator_name` in a Path config. To bypass a Backend-wide Authenticator in a Path config, use `authenticator_name: none`. This allows for a few possibilities:
  
  * A Backend config with a default / Backend-wide Authenticator that has:
    * specific Paths that do not require Authentication
    * specific Paths mapped to different Authenticators than the default
  
  * A Backend config with no Authenticator defined (so all Paths by default are unprotected) that has:
    * specific Paths mapped to different Authenticators than the default

See the example Backend configs below for more details.

## Authenticator Providers

Trickster's Authenticator feature currently supports Basic Auth and ClickHouse-compatible authentication. It was designed with extensibility in mind should there be value in adding additional Authentication providers.

## Basic Auth Provider

Basic Auth is supported by using `provider: basic` in the Authenticator config.

By setting the `showLoginForm: true` config (see `example_auth_1` in the blob below), Trickster will return a `WWW-Authenticate: Basic realm="custom-realm-name"` header on any request that requires but fails authentication, causing the login form to pop up. When `showLoginForm` is not present or non-true, Trickster responds with a `401 Unauthorized` but does not ask the Basic Auth login form to show.

The `realm` attribute value defaults to the Authenticator name (e.g., `example_auth_1`) but can be overridden with the `realm` config as in the example.

If the user data changes (e.g. updated users_file contents or updated embedded users list), you must send a SIGHUP or other means to reload the Trickster config before the new user pool is processed.

## ClickHouse Auth Provider

ClickHouse Auth is supported by using `provider: clickhouse` in the Authenticator config.

ClickHouse authentication is the same as Basic Auth, except you can also provide `user` and `password` URL params. 

## Example Authenticator Configs

```yaml
# NOTE: Required options unrelated to Authenticators have been omitted from this
# example. It does not represent a fully-functioning Trickster Config.
# See the 'examples' directory for working copy/paste config examples.

backends:
  backend01:
    provider: reverseproxy # authenticators work with all backend providers
    authenticator_name: example_auth_1 # protects backend01 with example_auth_1 authenticator
    origin_url: https://example.com
    paths:
      root:
        path: / # all requests are protected by example_auth_1 
        match_type: prefix
        handler: proxy

  backend02:
    provider: reverseproxy # no backend-wide authenticator
    origin_url: https://example.com
    paths:
      root:
        path: / # requests will be allowed without auth except the 2 Paths below
        match_type: prefix
        handler: proxy
      protected_a:
        path: /private/
        authenticator_name: example_auth_2 # example_auth_2 protects this path only
        handler: proxy
      protected_b:
        path: /admin/
        authenticator_name: example_auth_3 # example_auth_3 protects this path only
        handler: proxy

  backend03:
    provider: reverseproxycache
    authenticator_name: example_auth_1 # protects backend03 with example_auth_1 authenticator
    origin_url: https://example.com
    paths:
      path: / # requests will be challenged by example_auth_1 except the 2 Paths below
        match_type: prefix
        handler: proxy
      unprotected:
        path: /public/
        authenticator_name: none # requests to /public will be allowed without auth
        handler: proxy
      protected_a:
        path: /app/admin/
        authenticator_name: example_auth_2 # example_auth_2 protects this path, not auth_1
        handler: proxy

authenticators:
  # example_auth_1 loads users from a CSV and embeds a supplemental plaintext manifest
  # It also shows the login form to client browsers when login has failed
  example_auth_1:
    provider: basic # http basic auth (required)
    proxy_preserve: true # don't strip auth headers when proxying this request upstream
    users_file: /path/to/user-manifest.csv # optional users source file
    users_file_format: csv # required when users_file is set
    users: # optional embedded users manifest (username: credential)
      user1: red123
    users_format: plaintext # plaintext is the default format if this value is omitted
    config: # optional provider-specific configs
      showLoginForm: true # with basic auth, causes the browser to show the login form
      realm: custom-realm-name # realm would be example_auth_1 if not overridden here

  # example_auth_2 loads users from an htpasswd file (assumed bcrypted credentials)
  example_auth_2:
    provider: basic
    users_file: /path/to/user-manifest.htpasswd # optional users source file
    users_file_format: htpasswd # required when users_file is set

  # example_auth_3 loads users from the embedded users manifest, credentials already bcrypted
  example_auth_3:
    provider: basic
    users:
      user1: asf;j2ihj0h8vabjkwdqbv29hq
    users_format: bcrypt # credentials are already bcrypted

  # example_auth_4 loads users from the embedded manifest, credentials injected from env,
  # and supports ClickHouse query params (user/password) in addition to Basic Auth
  example_auth_4:
    provider: clikchouse
    users:
      user1: ${USER1_PASSWORD_ENV} # ${ENV_NAME} substitution is supported
```