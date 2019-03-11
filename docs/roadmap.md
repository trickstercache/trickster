# Trickster Roadmap

Our roadmap for Trickster is largely focused on a 1.0 release, which will have a completely refactored codebase. Trickster 1.0 will be more efficient and easily extensible. 

Trickster 1.0 will have the folowing enhancements:
- The application is refactored into Packages to simplify reuse
- Simplified hash collision prevention and pipelining (replacing channels with mutexes)
- Upstream Proxy interface to faciliate support for additional TSDB types
- Support for InfluxDB acceleration
- Full compliance with HTTP 1.0/1.1 RFC's for Proxy/Caching
- Simpler and more efficient Delta computations
- Caches per-origin instead of per-process
- Size-based cache quota
- Distributed Tracing support

## Timeline

### Q1 2019 - Trickster 1.0 Beta Release

We intend to provide a Trickster 1.0 Beta Release by the end of Q1 2019 that will include the majority of features listed above.

### Q2 2019 - Trickster 1.0 GA Release

We hope to provdie a Trickster 1.0 GA Release in the first half of Q2 2019 that includes all of the features listed above.

## How to Help

You can help by contributing to Trickster 1.0 on the `next` branch, or trying it out in your environment. Docker images for the latest Trickster 1.0 Beta release wiil be published under the `next` tag.

By giving Trickster 1.0 Beta a spin, you can help us identify and fix defects more quickly. Be sure to file issues if you find something wrong, using the `1.0` label. If you can reliably reproduce the issue, provide detailed steps so that developers can more easily root-cause the issue.

If you want to contribute to Trickster 1.0, take any of the issues labeled `1.0` that are not already assigned. Many of these have been outstanding for some time pending the Interface model, so now is great time to look at extending Trickster to work with your TSDB of choice.

## Thank You

We are so excited to share the Trickster 1.0 release, which will be a significant upgrade to 0.1. This is only possible through our great community of contributors and supporters. Thank you for getting us this far and helping us to get v1.0 shipped this spring!
