# Trickster 1.0 Beta

## What's Improved?

1.0 is a major improvement in over 0.1.x. Here's the quick rundown of what has been improved:

- Cache management is improved, with enhancements like a configurable max cache size and better metrics.
- Configuration now allows per-origin cache provider selection.
- The Delta Proxy is overhauled to be more efficient and performant.
- For Gophers: we've refactored the project into packages with a much improved structure.
- Also: The Cache Provider and Origin Proxy are exposed as Interfaces for easy extensibility.

### Still to Come in 1.0 (keep checking back!)
- InfluxDB support 
- Distributed Tracing support

## Breaking Changes

### Config File

Trickster 1.0 is incompatible with a 0.1.x config file. However, it can be made compatible with a few quick migration steps:

- Make a backup of your config file.
- Tab-indent the entire `[cache]` configuration block.
- Search/Replace `[cache` with `[cache.default` (no trailing square bracket).
- Unless you are using Redis, copy/paste the `[cache.default.index]` section from the [example.conf](../cmd/trickster/conf/example.conf) into your new config file under `[cache.default]` as in the example.
- Add a line with `[caches]` (unindented) immediately above the line with `[cache]`
- Under each of your `[origin configurations, add the following line 

    `cache_name = 'default'`

- For more information, refer to the [example.conf](../cmd/trickster/conf/example.conf), which is well-documented.

