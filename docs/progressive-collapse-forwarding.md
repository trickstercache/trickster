# What is Progressive Collapsed Forwarding?
Progressive Collapsed Forwarding (PCF) collapeses similar requests to a given origin and collapses them into one request. 
Eg.
- Request 1 comes from client A to an origin.
- While the response to the above request is being streamed through Trickster the exact same reqeuest, request 2 comes from client B, comes at the same time.
- If PCF is enabled Trickster will io stream request 2 to client B off of the same data from the original request.
- The data being served out of Trickster with PCF will then be as fast as the connection between client B and Trickster, allowing client B to catch up to the live point of the request.
- If Trickster is set to act as an object proxy cache any subsequent requests will be served out of cache as per normal after the origin request has completed.

This is useful for reducing the load on an origin server.

# How is Progressive Collapsed Forwarding different than other Forward Collapsing?
Essentially PCF is Forward Collapsing like other proxies provide but the request is not done sequentially; all of the data can be streamed back to each client at the same time and you do not have to wait for the original request to complete before the proxy begins responding. This removes the latency issues commonly found in Forward Collapsing solutions in other proxies.


# How do I enable Progressive Collapsed Forwarding?
When configuring path configs as described in `paths.md` you simply need to add `progressive_collapsed_forwarding = true` in any path config using the `proxy` or `proxycache` handlers.
Eg. 
```
        [origins.test.paths]
            [origins.test.paths.thing1]
                path = '/test_path1/'
                match_type = 'prefix'
                handler = 'proxycache'
                progressive_collapsed_forwarding = true

            [origins.test.paths.thing2]
                path = '/test_path2/'
                match_type = 'prefix'
                handler = 'proxy'
                progressive_collapsed_forwarding = true
```

This is also shown in `cmd/trickster/conf/example.conf`.

# How can I test Progressive Collapsed Forwarding?
An easy way to test PCF is to set up your favorite file server to host a large file(Lighttpd, Nginx, Apache WS, etc.), In Trickster turn on PCF for that path config and try make simultaneous requests.
If the networking between your machine and Trickster has enough bandwidth you should see both streaming at the equivalent rate as the origin request.

Eg.
- Run a Lighttpd instance or docker container on your local machine and make a large file available to be served
- Run Trickster locally
- Make multiple curl requests of the same object

You should see the speed limited on the origin request by your disk IO, and your speed between Trickster limited by Memory/CPU