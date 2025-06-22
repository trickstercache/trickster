# Request Body Handling Customizations

## Request Body Size Limiter

By default, the max allowed Request Body size is 10 MB. If the client request
body in a POST, PUT or PATCH are over 10 MB, the request will receive a
response of `413 Request Payload is too large`

You can change (or bypass) this limit in Frontend Config Section:

```yaml
frontend:
  max_request_body_size_bytes: 5120 # request bodies must be <= 5kb or returns 413
```

```yaml
frontend:
  max_request_body_size_bytes: 5120 # request bodies > 5kb truncated to 5kb
  truncate_request_body_too_large: true # truncate request bodies that are too large
```
