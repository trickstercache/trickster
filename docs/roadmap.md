# Trickster Roadmap

The roadmap for Trickster in 2021 focuses on delivering Trickster versions 2.0 and 2.1, as well as new supporting applications and cloud native integrations. Additional details for Q3 and Q4 will be provided as the year progresses.

## Timeline

### Q1 2021

- [ ] Trickster v2.0 Beta Release
  - [x] Common Time Series Format used internally for all TSDBs
  - [x] Universal HTTP Health Checker Package
  - [x] ALB with features for high availability and scatter/gather timeseries merge
  - [x] YAML config support
  - [x] Extended support for ClickHouse
  - [ ] Support for InfluxDB 2.0, Flux syntax and querying via Chronograf
  - [ ] Purge object from cache by path or key
  - [ ] Short-term caching of non-timeseries read-only queries (e.g., generic SELECT statements)
  - [x] Support Brotli encoding over the wire and as a cache compression format
  
- [x] Submit Trickster for CNCF Sandbox Consideration

### Q2 2021

- [ ] Trickster v2.0 GA Release
  - [ ] Documentation overhaul using MkDocs with Release deployment automation
  - [ ] Migrate integration tests infrastructure as needed to easily integrate with related CNCF projects.

- [ ] Trickster v2.1 Beta Release
  - [ ] Support for ElasticSearch
  - [ ] Support operating as an adaptive, front-side cache for Grafana, including its UI, API's, and accelerating any supported timeseries datasources.
  - [ ] Better support for operating in front of Thanos
  - [ ] Ability to parallelize large timerange queries by scatter/gathering smaller sections of the main timerange.
  - [ ] Additional Rules Engine capabilities for more complex request routing
  - [ ] Grafana-style environment variable support
  - [ ] Subdirectory (e.g., `/etc/trickster.conf.d/`) support for chained config files

- [ ] Register Official Docker Hub Repositories

### Q3 2021

- [ ] Trickster v2.1 GA Release

### Q4 2021

- [ ] Trickster v2.2 Beta Release

## Get Involved

You can help by contributing to Trickster, or trying it out in your environment.

By giving Trickster a spin, you can help us identify and fix defects more quickly. Be sure to file issues if you find something wrong. If you can reliably reproduce the issue, provide detailed steps so that developers can more easily root-cause the issue.

If you want to contribute to Trickster, we'd love the help. Please take any issue that is not already assigned as per the contributing guidelines, or check with the maintainers to find out how best to get involved.

## Thank You

We are so excited to share the Trickster with the community. This is only possible through our great community of contributors, users and supporters. Thank you for all you in making this project a success!
