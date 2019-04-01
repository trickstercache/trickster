# Trickster 1.0 Beta

## What's Improved?

1.0 is a major improvement in over 0.1.x. Here's the quick rundown of what has been improved:

- Cache management is improved, with enhancements like a configurable max cache size and better metrics.
- Configuration now allows per-origin cache provider selection.
- The Delta Proxy is overhauled to be more efficient and performant.
- For Gophers: we've refactored the project into packages with a much more cohesive structure, so it's much easier for you to contribute.
- Also: The Cache Provider and Origin Proxy are exposed as Interfaces for easy extensibility.
- InfluxDB support (very experimental)

### Still to Come in 1.0 (keep checking back!)
- Distributed Tracing support

## How to Try Trickster 1.0

The Docker image is available at `tricksterio/trickster:1.0-beta`, or see the Releases for downloadable binaries.

We'd love your help testing Trickster 1.0, as well as contributing any improvements or bug fixes. Thank you!

## Breaking Changes from 0.1.x

### Origin Selection using Query Parameters

In a multi-origin setup, Trickster 1.0 no longer supports the ability to select an Origin using Query Parameters. Trickster 1.0 continues to support Origin Selection via URL Path or Host Header as in 0.1.x. 

### Config File

Trickster 1.0 is incompatible with a 0.1.x config file. However, it can be made compatible with a few quick migration steps:

- Make a backup of your config file.
- Tab-indent the entire `[cache]` configuration block.
- Search/Replace `[cache` with `[caches.default` (no trailing square bracket).
- Unless you are using Redis, copy/paste the `[caches.default.index]` section from the [example.conf](../cmd/trickster/conf/example.conf) into your new config file under `[caches.default]`, as in the example.
- Add a line with `[caches]` (unindented) immediately above the line with `[caches.default]`
- Under each of your `[origins.<name>]` configurations, add the following line 

    `cache_name = 'default'`

- For more information, refer to the [example.conf](../cmd/trickster/conf/example.conf), which is well-documented.

