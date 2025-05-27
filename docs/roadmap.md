# Trickster Roadmap

The roadmap for Trickster in 2025 focuses on delivering Trickster versions 2.0 and 2.1, as well as supporting new time series applications and cloud native integrations. Additional details for Q3 and Q4 will be provided as the year progresses.

## Timeline

### May 2025

- [x] Trickster v2.0 Beta 3 Release
  - [x] Common Time Series Format used internally for all TSDBs
  - [x] Universal HTTP Health Checker Package
  - [x] YAML config support
  - [x] Purge object from cache by path or key
  - [x] Short-term caching of non-timeseries read-only queries (e.g., generic SELECT statements)
  - [x] Support Zstd and Brotli encoding over the wire and as a cache compression format
  - [x] Ability to parallelize large timerange queries by scatter/gathering smaller sections of the main timerange.
  - [x] Cache Chunking
  - [x] Application Load Balancer
  - [x] Performant HTTP Router designed specifically for Proxies
  - [x] Resolve all known Race Conditions
  - [X] Docker & Helm Charts overhauled for Trickster 2.0
  - [X] Additional Rules Engine capabilities for more complex request routing
  - [x] Extended support for ClickHouse
  - [x] Support for InfluxDB 2.0 and Flux query syntax / payloads

### June 2025

- [ ] Trickster v2.0 Beta 4 Release
  - [ ] More easily-importable Trickster packages by other projects
  - [ ] Support for MySQL as Time Series
  - [ ] Support for InfluxDB 3.0
  - [ ] Support for Autodiscovery (e.g., Kubernetes Pod Annotations)

### July 2025

- [ ] Trickster v2.0 GA Release
  - [ ] Overhaul Documentation for Trickster 2.0

### Late Q3/Early Q4 2025

- [ ] Trickster v2.1 Beta Release
  - [ ] Kube Gateway API support

## Get Involved

You can help by contributing to Trickster, or trying it out in your environment.

By giving Trickster a spin, you can help us identify and fix defects more quickly. Be sure to file issues if you find something wrong. If you can reliably reproduce the issue, provide detailed steps so that developers can more easily root-cause the issue.

If you want to contribute to Trickster, we'd love the help. Please take any issue that is not already assigned as per the contributing guidelines, or check with the maintainers to find out how best to get involved.

## Thank You

We are so excited to share the Trickster with the community. This is only possible through our great community of contributors, users and supporters. Thank you for all you in making this project a success!
