# MySQL Compatibility Corpus

`v1.json` is the executable compatibility specification for the MySQL SQL
analyzer. A corpus version is immutable once released; incompatible schema or
expectation changes require a new versioned file.

The corpus records exact SQL after Grafana macro expansion. It intentionally
uses the existing query examples rather than requiring Query Inspector exports
from every dashboard panel or paired Builder/Code mode captures. Those two
approved omissions are recorded in the corpus metadata.

The documented minimum value for `$__interval` cases is **1 minute**. Corpus
queries also cover fixed intervals, native `DATETIME`/`TIMESTAMP` values,
epoch-second integers, and epoch-nanosecond integers.

Inclusive `BETWEEN` time filters remain object-cache-only because their upper
bound can expose a partial final bucket. Delta-cacheable raw-time predicates
must use an inclusive lower bound and an exclusive upper bound. Every DPC case
records its cadence, phase, units, bounds, output column, group columns,
canonical SQL characteristics, and extent-rendering requirement.
