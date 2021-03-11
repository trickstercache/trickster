# Rule Backend

The Rule Backend is not really a true Backend; it only routes inbound requests to other configured Backends, based on how they match against the Rule's cases.

A Rule is a single inspection operation performed against a single component of an inbound request, which determines the Next Backend to send the request to. The Next Backend can also be a rule Backend, so as to route requests through multiple Rules before arriving at a true Backend destination.

A rule can optionally rewrite multiple portions of the request before, during and after rule matching, by using [request rewriters](./request_rewriters.md), which allows for powerful and limitless combinations of request rewriting and routing.

## Rule Parts

A rule has several required parts, as follows:

Required Rule Parts

- `input_source` - The part of the Request the Rule inspects
- `input_type` - The source data type
- `operation` - The operation taken on the input source
- `next_route` - The Backend Name indicating the default next route for the Rule if no matching cases. Not required if `redirect_url` is provided.
- `redirect_url` - The fully-qualified URL to issue as a 302 redirect to the client in the default case. Not required if `next_route` is provided.

Optional Rule Parts

- `input_key` - case-sensitive lookup key; required when the source is `header` or URL `param`
- `input_encoding` - the encoding of the input, which is decoded prior to performing the operation
- `input_index` - when > -1, the source is split into parts and the input is extracted from parts\[input_index\]
- `input-delimiter` - when input_index > -1, this delimiter is used to split the source into parts, and defaults to a standard space (' ')
- `ingress_req_rewriter name` - provides the name of a Request Rewriter to operate on the Request before rule execution.
- `egress_req_rewriter name` - provides the name of a Request Rewriter to operate on the Request after rule execution.
- `nomatch_req_rewriter name` - provides the name of a Request Rewriter to operate on the Request after rule execution if the request did not match any cases.
- `max_rule_executions` - limits the number of rules a Request is passed through, and aborts with a 400 status code when exceeded. Default is 16.

### input_source permitted values

| source name   | example extracted value                              |
| ------------- | ---------------------------------------------------- |
| url           | <https://example.com:8480/path1/path2?param1=value>  |
| url_no_params | <https://example.com:8480/path1/path2>               |
| scheme        | https                                                |
| host          | example.com:8480                                     |
| hostname      | example.com                                          |
| port          | 8480 (inferred from scheme when no port is provided) |
| path          | /path1/path2                                         |
| params        | ?param1=value                                        |
| param         | (must be used with input_key as described below)     |
| header        | (must be used with input_key as described below)     |

### input_type permitted values and operations

| type name          | permitted operations  |
| ------------------ | ----------------------|
| string  (default)  | prefix, suffix, contains, eq, md5, sha1, modulo, rmatch |
| num                | eq, le, ge, gt, lt, modulo |
| bool               | eq |

## Rule Cases

Rule cases define the possible values are able to alter the Request and change the next route.

## Case Parts

Required Case Parts

- `matches` - A string list of values applicable to this case.
- `next_route` - The Backend Name indicating the  next route for the Rule when a request matches this Case. Not required if `redirect_url` is provided.
- `redirect_url` - The fully-qualified URL to issue as a 302 redirect to the client when the Request matches this Case. Not required if `next_route` is provided.

Optional Case Parts

- `req_rewriter name` - provides the name of a Request Rewriter to operate on the Request when this case is matched.

## Example Rule - Route Request by Basic Auth Username

In this example config, requests routed through the `/example` path will be compared against the rules and routed to either the Reader cluster or the Writer cluster. Curling `http://trickster-host/example/path` would route to the reader or writer cluster based on a provided Authorization header.

```yaml
rules:
  example-user-router:
    # default route is reader cluster
    next_route: example-reader-cluster

    input_source: header
    input_key: Authorization
    input_type: string
    input_encoding: base64 # Authorization: Basic <base64string>
    input_index: 1         # Field 1 is the <base64string>
    input_delimiter: ' '   # Authorization Header field is space-delimited
    operation: prefix      # Basic Auth credentials are formatted as user:pass,
                           # so we can check if it is prefixed with $user:
    cases:
      writers:
        matches: # route johndoe and janedoe users to writer cluster
          - 'johndoe:'
          - 'janedoe:'
        next_route: example-writer-cluster

backends:
  example:
    provider: rule
    rule_name: example-user-router

  example-reader-cluster:
    provider: rpc
    origin_url: 'http://reader-cluster.example.com'

  example-writer-cluster:
    provider: rpc
    origin_url: 'http://writer-cluster.example.com'
    path_routing_disabled: true  # restrict routing to this backend via rule only, so
                                 # users cannot directly access via /example-writer-cluster/
```

## Example Rule - Route Request by Path Regex

In this example config, requests routed through the `/example` path will be compared against the rules and routed to either the Reader cluster or the Writer cluster. Curling `http://trickster-host/example/reader` and `http://trickster-host/example/writer` would route to the reader or writer cluster by matching the path.

```yaml
rules:
  example-user-router:
    # default route is reader cluster
    next_route: example-reader-cluster

    input_source: path
    input_type: string
    operation: rmatch      # perform regex match against the path to see if it matches 'writer
    operation_arg: '^.*\/writer.*$'
    cases:
      rmatch-true:
        matches: [ 'true' ] # rmatch returns true when the input matches the regex; update next_route
        next_route: example-writer-cluster

backends:
  example:
    provider: rule
    rule_name: example-user-router

  example-reader-cluster:
    provider: rpc
    origin_url: 'http://reader-cluster.example.com'

  example-writer-cluster:
    provider: rpc
    origin_url: 'http://writer-cluster.example.com'
    path_routing_disabled: true  # restrict routing to this backend via rule only, so
                                 # users cannot directly access via /example-writer-cluster/
```