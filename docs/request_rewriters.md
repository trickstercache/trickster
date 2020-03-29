# Request Rewriters

A Request Rewriter is named series of instructions that modifies any part of the incoming HTTP request. Request Rewriters are used in various parts of the Trickster configuration to make scoped changes. For example, a rewriter can modify the path, headers, parameters, etc. of a URL using mechanisms like search/replace, set and append.

In a configuration, request rewriters are represented as map of instructions, which themselves are represented as a list of string lists, in the following format:

```toml
[rewriters]
  [rewriters.example_rewriter]
  instructions = [
    [ 'header', 'set', 'Cache-Control', 'max-age=60 ], # instruction 0
    [ 'path', 'replace', '/cgi-bin/', '/' ],           # instruction 1
  ]
```

In this case, any other configuration entity that supports mapping to a rewriter by name can do so with by referencing `example_rewriter`.

## header

`header` rewriters modify a header with a specific name and support the following operations.

### header set

`['header', 'set', 'Header-Name', 'header value']`

### header replace

`['header', 'replace', 'Header-Name', 'search value', 'replacement value']`

### header delete

`['header', 'delete', 'Header-Name']`

### header append

`['header', 'append', 'Header-Name', 'additional header value']`

## path

`path` rewriters modify the full or partial path and support the following operations.

### path set

`['path', 'set', '/new/path']` sets the entire request path to `/new/path`

`['path', 'set', 'awesome', 0 ]` sets the first part of the path to 'awesome'. For example, `/new/path` => `/awesome/path`

### path replace

`['path', 'replace', 'search', 'replacement']` search replaces against the entire path scalar

`['path', 'replace', 'search', 'replacement', 1]` search replaces against the second part of the path. For example `/my/example-search/path` => `/my/example-replacement/path`

## param

`param` rewriters modify the URL parameter of the specified name, and support the following operations.

### param set

`['param', 'set', 'paramName', 'new param value']`

### param replace

`['param', 'replace', 'paramName', 'search value', 'replacement value']`

### param delete

`['param', 'delete', 'paramName']`

### param append

`['param', 'append', 'paramName', 'additional param value']`

This operation will add an additional parameter of the provided name, with the new value, to the request URL, regardless of whether the parameter already exists of the same or different value.

## params

`params` rewriters update the entire URL parameter collection as a URL-encoded scalar string.

### params set

`['params', 'set', 'param1=value1&param2=value2']`

To clear the URL parameters, use `['params', 'set', '']`

### params replace

`['params', 'replace', 'search value', 'replacement value']`

## method

`method` rewriters update the HTTP request's method

### method set

`['method', 'set', 'GET']`

## host

`host` rewriters update the HTTP Request's host - defined as the hostname:port.

### host set

`['host', 'set', 'my.new.hostname:9999']`

`['host', 'set', 'my.new.hostname']` will use port 80/443 depending upon the URL scheme

### host replace

`['host', 'replace', ':8480', '']`

`['host', 'replace', ':443', ':8443']`

`['host', 'replace', 'example.com', 'tricksterproxy.io']`

## hostname

`hostname` rewriters update the HTTP Request's hostname, without respect to the port.

### hostname set

`['host', 'set', 'my.new.hostname']`

### hostname replace

`['host', 'replace', 'example.com', 'tricksterproxy.io']`

## port

`port` rewriters update the HTTP Request's port, without respect to the hostname.

### port set

`['port', 'set', '8480']`

### port replace

`['port', 'replace', '8480', '']`

### port delete

`['port', 'delete']`

## scheme

`scheme` rewriters update the HTTP Request's scheme (`http` or `https`).

### scheme set

`['scheme', 'set', 'https']`
