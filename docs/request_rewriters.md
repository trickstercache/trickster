# Request Rewriters

A Request Rewriter is a named series of instructions that modifies any part of the incoming HTTP request. Request Rewriters are used in various parts of the Trickster configuration to make scoped changes. For example, a rewriter can modify the path, headers, parameters, etc. of a URL using mechanisms like search/replace, set and append.

In a configuration, request rewriters are represented as map of instructions, which themselves are represented as a list of string lists, in the following format:

```yaml
request_rewriters:
  example_rewriter:
    instructions:
      - [ 'header', 'set', 'Cache-Control', 'max-age=60' ], # instruction 0
      - [ 'path', 'replace', '/cgi-bin/', '/' ],            # instruction 1
      - [ 'chain', 'exec', 'remove_accept_encoding' ]       # instruction 2

  remove_accept_encoding:
    instructions:
      - [ 'header', 'delete', 'Accept-Encoding' ] # instruction 0
```

In this case, any other configuration entity that supports mapping to a rewriter by name can do so with by referencing `example_rewriter` or `remove_accept_encoding`. Note that `example_rewriter` executes `remove_accept_encoding` using the `chain` instruction.

## Where Rewriters Can Be Used

Rewriters are exposed as optional configurations for the following configuration constructs:

In a `backend` config, provide a `req_rewriter_name` to rewrite the Request using the named Request Rewriter, before it is handled by the Path route.

In a `path` config, provide a `req_rewriter_name` to rewrite the Request using the named Request Rewriter, before it is handled by the Path route.

In a `rule` config, provide `ingress_req_rewriter_name`, `egress_req_rewriter_name` and/or `nomatch_req_rewriter_name`  configurations to rewrite the Request using the named Request Rewriter. The meaning of Ingress and Egress, in this case, are scoped to a Request's traversal through the Rule in which these configuration values exist, and is unrelated to the wire traversal of the request. For ingress and egress, the rewriter is executed before or after, respectively, it is handled by the Rule (including any modifications made by a matching rule case). The No-Match Request Rewriter is only executed when the request does not match to any defined case.

In a Rule's `case` configurations, provide `req_rewriter_name`. If there is a Rule Case match when executing the Rule against the incoming Request, the configured rewriter will execute on the Request before returning control back to the Rule to execute any configured egress request rewriter and hand the Request off to the next route.

In a Request Rewriter instruction using the `chain` instruction type. Provide the Rewriter Name as the third argument in the instruction  as follows: `[ 'chain', 'exec', '$rewriter_name']`. See more information [below](#chain).

## Instruction Construction Guide

### header

`header` rewriters modify a header with a specific name and support the following operations.

#### header set

`header set` will set a specific header to a specific value.

`['header', 'set', 'Header-Name', 'header value']`

#### header replace

`header replace` performs a search/replace function on the Header value of the provided Name

`['header', 'replace', 'Header-Name', 'search value', 'replacement value']`

#### header delete

`header delete` removes, if present, the Header of the provided Name

`['header', 'delete', 'Header-Name']`

#### header append

`header append` appends an additional value to Header in the format of `value1[, value2=subvalue, ...]`

`['header', 'append', 'Header-Name', 'additional header value']`

### path

`path` rewriters modify the full or partial path and support the following operations.

#### path set

`['path', 'set', '/new/path']` sets the entire request path to `/new/path`

`['path', 'set', 'awesome', 0 ]` sets the first part of the path (zero-indexed, split on `/`) to 'awesome'. For example, `/new/path` => `/awesome/path`

#### path replace

`['path', 'replace', 'search', 'replacement']` search replaces against the entire path scalar

`['path', 'replace', 'search', 'replacement', 1]` search replaces against the second part of the path; For example `/my/example-search/path` => `/my/example-replacement/path`

### param

`param` rewriters modify the URL Query Parameter of the specified name, and support the following operations

#### param set

`param set` sets the URL Query Parameter of the provided name to the provided value

`['param', 'set', 'paramName', 'new param value']`

#### param replace

`param replace` performs a search/replace function on the URL Query Parameter value of the provided name

`['param', 'replace', 'paramName', 'search value', 'replacement value']`

#### param delete

`param delete` removes, if present, the URL Query Parameter of the provided name

`['param', 'delete', 'paramName']`

#### param append

`param append` appends the provided name and value to the URL Query Parameters regardless of whether a parameter name already exists with the same or different value.

`['param', 'append', 'paramName', 'additional param value']`

### params

`params` rewriters update the entire URL parameter collection as a URL-encoded scalar string.

#### params set

`params set` will replace the Request's entire URL Query Parameter encoded string with the provided value. The provided value is assumed to already be URL-encoded.

`['params', 'set', 'param1=value1&param2=value2']`

To clear the URL parameters, use `['params', 'set', '']`

#### params replace

`params replace` performs a search/replace operation on the url-encoded query string. The search and replacement values are assumed to already be URL-encoded.

`['params', 'replace', 'search value', 'replacement value']`

### method

`method` rewriters update the HTTP request's method

#### method set

`method set` sets the Request's HTTP Method to the provided value. This value is not currently validated against known HTTP Method. The instruction should be configured to include a known and properly-formatted (all caps) HTTP Method.

`['method', 'set', 'GET']`

### host

`host` rewriters update the HTTP Request's host - defined as the hostname:port, as expressed in the Request's `Host` header.

#### host set

`host set` sets the Request's `Host` header to the provided value.

`['host', 'set', 'my.new.hostname:9999']`

`['host', 'set', 'my.new.hostname']` Trickster will assume a standard source port 80/443 depending upon the URL scheme

#### host replace

`host replace` performs a search/replace operation on the Request's `Host` Header.

`['host', 'replace', ':8480', '']`

`['host', 'replace', ':443', ':8443']`

`['host', 'replace', 'example.com', 'trickstercache.org']`

### hostname

`hostname` rewriters update the HTTP Request's hostname, without respect to the port.

#### hostname set

`hostname set` sets the Request's hostname, without changing the port.

`['hostname', 'set', 'my.new.hostname']`

#### hostname replace

`hostname replace` performs a search/replace on the Request's hostname, without respect to the port.

`['hostname', 'replace', 'example.com', 'trickstercache.org']`

### port

`port` rewriters update the HTTP Request's port, without respect to the hostname.

#### port set

`port set` sets the Request's port.

`['port', 'set', '8480']`

#### port replace

`port replace` performs a search/replace on the port, as if it was a string. The search and replacement values must be integers and are not validated.

`['port', 'replace', '8480', '']`

#### port delete

`port delete` removes the port from the Request. This will cause the port to be assumed based on the URL scheme.

`['port', 'delete']`

### scheme

`scheme` rewriters update the HTTP Request's scheme (`http` or `https`).

#### scheme set

`scheme set` sets the scheme of the HTTP Request URL. This must be `http` or `https` in lowercase, and is not validated.

`['scheme', 'set', 'https']`

### chain

`chain` rewriters do not directly rewrite the request, but execute other rewriters' instructions before proceeding with the current rewriter's remaining instructions (if any). You can create a rewriter with some reusable functionality and include that in other rewriters with a chain exec. Or you can define a rewriter that is just a list of other chained rewriters. Note that there is currently no validation of the configuration to prevent infinite cyclic chained rewriter calls. There is, however, a hard limit of 32 chained rules before a request will stop rewriting and proceed with being served by the backend.

`chain exec` executes the supplied rewriter name. Trickster will error at startup if the rewriter name is invalid. An example is provided in the sample yaml config at the top of this article.

`['chain', 'exec', 'example_rewriter']`

