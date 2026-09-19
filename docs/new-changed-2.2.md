# Trickster 2.2

Trickster 2.2 extends many of the new features introduced in 2.1, while adding support for delta-caching several new TSDB's that use the Postgres Query Dialect. 

Trickster 2.2 just recently began development, so many of the planned features are still being designed or are under development.

## Load Balancing and Scaling

**PLANNED** - **Layer 4 Load Balancing** - We extend our HTTP L7 ALB to support Layer 4 as well. Supported mechanisms beyond Round Robin are TBD.

**PLANNED** - We now support sticky sessions for the Round Robin Load Balancer mechanism. More mechanisms TBD, based on whether any new ones are added for L4.

**PLANNED** - We now provide a request rate limiter based on request attributes. it can be attached at the listener, backend, and path levels, with most specific winning.

**PLANNED** - We've also added IP Access Control Lists to restrict access to certain backend resources by IP. it can be attached at the listener, backend, and path levels, with most specific winning.

## New Acceleration-supported TSDBs

All three of these newly-supported providers consume a new `pgwire` package for the postgres wire protocol, along with a common lexer and per-dialect parsers. Any future Postgres-compatible providers Trickster supports will reuse this for faster bootstrapping.

**TimescaleDB** - You can now accelerate TimescaleDB with the delta proxy cache! If you are tired of playing whack-a-mole with new continuous aggregates to manage performance, Trickster can stop the madness. Even better - any Postgres-compatible database can be fronted by Trickster for a `SELECT` result cache.

**PLANNED** - **GrepTimeDB** - We've added GrepTimeDB as an acceleration-supported backend time series provider.

**PLANNED** - **QuestDB** - And we also now support accelerating QuestDB.

**PLANNED** - Better support for TSM with a distributed Mimir system.

## HTTP Reverse Proxy Cache & Streaming

**PLANNED** - **Media over QUIC (MoQ)** -- In Trickster 2.1, we introduced support for HTTP/3 (QUIC). We now offer support for MoQ Relaying through the reverse proxy cache.
