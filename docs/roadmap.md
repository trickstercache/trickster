# Trickster Roadmap

The roadmap for Trickster in the remainder of 2026 focuses on delivering versions 2.2 and 2.3; as well as supporting new time series applications and cloud native integrations.  Trickster v2.0 was released in Q2 and v2.1 was released in Q3.

## Timeline

### Q3 2026

- [x] Trickster v2.1 Release
  - [x] Auto-reload config + `conf.d` directory support
  - [x] Support for [ALB Pool Autodiscovery](./alb-autodiscovery.md)
  - [x] Support for MySQL as Time Series
  - [x] Support for Graphite
  - [x] Support for InfluxDB 3.x (HTTP v3 API + Flight SQL)
  - [x] Support for Druid
  - [x] Kube Ingress/Gateway API support
  - [x] Support for HTTP/3 (QUIC) and improved HTTP/1.1 and HTTP/2 conformance

### Q4 2026

- [ ] Trickster v2.2 Release
  - [x] Support for accelerating TimescaleDB
  - [x] Support for accelerating GreptimeDB
  - [x] Support for accelerating QuestDB
  - [x] Support for accelerating VictoriaMetrics
  - [x] Support Access Control Lists (ACLs) for IPv4 and IPv6
  - [x] Support Geo ACLs (IP -> Location translation and restricting by location)
  - [x] Support Rate Limiting w/ bucketing on configurable request attributes
  - [x] Support ALB Sticky Sessions
  - [x] Support L4 Load Balancing
  - [x] Support HTTP `QUERY` Method
  - [ ] Support Media over Quick (MoQ) Relaying
  - [x] Improved support for Time Series Merge on distributed Mimir and Thanos deployments

- [ ] Trickster v2.3 Release
  - [ ] Expand Authenticator to more providers (OIDC, JWT, API Key, mTLS, etc.)
  - [ ] Expand Prometheus support to more managed services (Azure, GCP)
  - [ ] Expand Time Series Merge to more providers (VictoraMetrics, InfluxDB, etc.)
  - [ ] Support emergent Caching RFCs (9875, 10036, 9209, draft-ietf-httpbis-no-vary-search-10)
  - [ ] S3 Range Caching
  - [ ] Compression Dictionaries
  - [ ] JSON marshaling/unmarshaling Performance via v2 / jsontext
  - [ ] Web Application Firewall
  - [ ] Publish Trickster installer packages for deb, dpkg, rpm, casks via the CI/CD Pipeline
  - [ ] Streaming Video Enhancements (low-latency DASH/HLS, content steering, AWS QUIC LB support)

### Ongoing

- [ ] Updated Grafana Dashboard for Trickster Metrics
- [ ] More easily-importable Trickster packages by other projects

## Get Involved

You can help by contributing to Trickster, or trying it out in your environment.

By giving Trickster a spin, you can help us identify and fix defects more quickly. Be sure to file issues if you find something wrong. If you can reliably reproduce the issue, provide detailed steps so that developers can more easily root-cause the issue.

If you want to contribute to Trickster, we'd love the help. Please take any issue that is not already assigned as per the contributing guidelines, or check with the maintainers to find out how best to get involved.

## Thank You

We are so excited to share the Trickster with the community. This is only possible through our great community of contributors, users and supporters. Thank you for all you in making this project a success!
