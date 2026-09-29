# Per-Query Time Series Instructions

Beginning with Trickster v1.1, certain features like Fast Forward can be toggled a per-query basis, to assist with compatibility in your environment. This allows the drafters of a query to have some say over toggling these features on queries they find to have issues running through Trickster. This is done by adding directives via query comments. For example, in Prometheus, you can end any query with `# any comment following a hashtag`, so you can place the per-query instructions there.

## Supported Per-Query Instructions

### Fast Forward Disable

Instruction `trickster-fast-forward`

Supported for: Prometheus (other time series do not currently implement Fast Forward)

Usage: `go_goroutines{job="trickster"}  # trickster-fast-forward:off`

Notes: This can only be used to disable fast forward. A value of `on` will have no effect.

### Volatile Window

Instruction `trickster-volatile-window`

Supported for: All time series backends

Usage: `SELECT time, count(*) FROM table  -- trickster-volatile-window:120`

Notes: This overrides the backend's `volatile_window` for this query with the specified value, in whole seconds or as a duration such as `90s`.
