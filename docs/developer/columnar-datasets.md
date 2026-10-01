# Columnar DataSets

Every time series provider and native SQL listener holds its data in Trickster's Common Time Series Format, the [`dataset.DataSet`](../../pkg/timeseries/dataset/dataset.go). It is what the caches store, what merges and crops work on, and what each provider's marshaler writes back out to clients. This guide covers how a DataSet holds its data, how to read it without allocating, and how to write a response from it. [Streaming Unmarshalers](./streaming-unmarshalers.md) covers building one from an upstream response.

## Layout

A DataSet holds `Results`, each a `SeriesList` of `Series`. A `Series` is a header (its name, tags and field definitions) and its rows, which it holds by column in one or more `Segment`s:

- A `Segment` holds an epoch per row, and one `Column` per value field.
- A `Column` holds each value in an 8-byte cell: the value itself, or, for a bytes kind, an offset and length into the column's data.
- The `Kind` of a column's values is set per column, or per value when a column is `KindMixed`.

| Kind | Holds | `Value` returns |
| --- | --- | --- |
| `KindNull` | a missing value | `nil` |
| `KindBool`, `KindInt64`, `KindUint64`, `KindFloat64` | the value in its cell | `bool`, `int64`, `uint64`, `float64` |
| `KindString` | text, in the column's data | `string` |
| `KindBytes` | opaque bytes, such as a row blob or raw JSON | `[]byte` |
| `KindNumber` | a number's literal text, kept as the origin wrote it | `json.Number` |
| `KindExt` | any other value, boxed beside the cells | the value |

Only `KindExt` values hold pointers, so the garbage collector never scans the rest. A DataSet that a `Builder` built cuts every series' arrays from a few shared slabs, so a series costs a handful of slices rather than an allocation per point, and `Size()` reports the arrays' real size.

Segments are read-only. Views share them instead of copying: `View`, `FullView` and `CroppedView` return DataSets whose series read their parents' memory, and never modify their inputs. Merges of disjoint extents, such as a partial hit's new rows before or after the cached ones, join a series' Segments rather than copying the rows into one, until a series holds more than eight, when it's compacted. So a series may hold its rows in several Segments, and **everything that reads a series must read all its Segments**. `Series.HasParts` and `DataSet.HasParts` report whether one does, and `DataSet.Flat` returns a view whose series each hold one.

## Reading Rows

Read values in place, by column and row, with the `Segment`'s readers:

- `KindAt(c, i)` returns the kind of column `c`'s value at row `i`.
- `Float64`, `Int64`, `Uint64` and `Bool` return a value of that kind.
- `Text` returns a `KindString` value as a string that shares the column's memory, without copying it.
- `Bytes` returns a bytes kind's value, which must not be modified.
- `Value` returns the value boxed. Boxing allocates for most values, so keep `Value` off hot paths, as a fallback for kinds a reader doesn't handle.
- `NumCols` returns the Segment's column count. A row may be narrower than its series' value fields; treat a missing column as null.

To read a series' rows in the order it holds them, loop over its Segments, then their rows:

```go
segs := series.Segments()
for k := range segs {
	seg := &segs[k]
	for i := range seg.Len() {
		ep := seg.Epoch(i)
		// read seg's columns at row i
	}
}
```

`Result.Rows(order)` yields a result's rows across its series, merged by epoch. Each series must be sorted, which `Series.IsSorted` reports; a series that isn't needs a sort instead. Within an epoch, rows come in series order, or as `RowOrder.Compare` orders them:

- `RowOrder{}` merges ascending, with no buffering and no comparisons beyond the epochs.
- `RowOrder{Descending: true}` merges newest first, reading each series backward. Rows of one series that share an epoch, such as the boundary of two Segments, then come in reverse. Set `Compare` to `dataset.CompareStored` to keep them in stored order.
- `Compare` breaks an epoch's ties, sorting each epoch's rows stably. End it with `dataset.CompareStored`, so rows tied on every term keep the order a stable sort of the stored rows would give them.

`Row` carries the row's `Series`, its `Seg` and `Index`, and its `SeriesIndex` in the result. `Series.RowAt(i)` finds a series' row `i`, and `Segments.Keep(mask)` returns the rows a mask keeps, without copying when it keeps them all. `Point` and `Points` only build series, through `NewSeries` and `SetPoints`; tests that compare rows as Points read them with [`testutil/dspoints`](../../pkg/testutil/dspoints/dspoints.go).

## Writing Responses

A provider's `MarshalTimeseriesWriter` writes a DataSet in the provider's wire format. These are the patterns the providers' writers share. Each is in at least one provider, linked below.

**Lay out each series once.** Everything a row's output takes from its series is the same for each of its rows: its tags, quoted or encoded; its JSON keys; which column each output position reads. Work these out once per series, before the rows, and keep them in a slice indexed by the series' position. The [GreptimeDB](../../pkg/backends/greptimedb/model/marshal.go) and [InfluxDB 3 SQL](../../pkg/backends/influxdb/sql/marshal.go) writers lay out each series' cells, and the [ClickHouse](../../pkg/backends/clickhouse/model/layout.go) writer each output position. Series that share their fields can share a layout's keys, as the [Druid](../../pkg/backends/druid/model/marshal.go) writer's do.

**Check before the first byte.** When a value can't be written, such as a NaN in JSON, nothing should be. Check each column a response writes before writing anything: `Segment.CheckColumnJSON(c)` skips kinds that always encode and scans a float column without boxing.

**Read typed values.** Switch on `KindAt` and use the typed reader. Format numbers with `strconv.Append*`, and times with `epoch.AppendCanonicalTime` or `Epoch.AppendFormat`, which don't allocate. `Segment.AppendJSON` writes a value as `encoding/json` would.

**Order rows without sorting them when you can.**
- With no `ORDER BY`, write the rows as stored, series by series.
- When the order starts with time and every series is sorted, merge with `Result.Rows`. Later terms, and a descending order, go in `Compare`, ending with `CompareStored`. If they're in more than one result, a writer can merge them all as one result of every series in stored order.
- Otherwise build a reference per row, and sort the references stably by comparisons that read typed values.
- When ties within an epoch are broken by something constant per series, such as its tags, sort the series once instead of comparing per row. The Druid native writer puts its series in tag order, so a merge's ties by series order are its ties by tags.

**Compare totally.** A comparison must be a total order, or a sort's output depends on the order of its input. Floats need a place for NaN: the Arrow batches for Flight SQL order floats by IEEE 754 totalOrder, as DataFusion does, and the InfluxDB 3 SQL writer sorts NaN after every number.

**Write through a `ChunkWriter`.** `tbytes.NewChunkWriter(w)` appends to a pooled buffer. Call `FlushIfFull` after each row, which writes the buffer once it holds 32 KiB, and `Close` at the end, which writes the rest and returns the buffer to its pool.

[Arrow record batches](../../pkg/timeseries/dataset/arrow/arrow.go) for Flight SQL follow the same patterns, building a batch at a time.

### Testing a Writer

When you change a writer, keep the old one in a `_test.go` file as an oracle, and compare their output byte for byte over random DataSets. The InfluxDB 3 SQL, Druid and GreptimeDB tests (`marshal_order_test.go`) do this. Cover:

- every ordering shape: none, time either way, time then other terms, and orders that don't start with time;
- rows tied across series, and within a series at a Segment boundary;
- a series held in parts, which [`testutil/parts.Of`](../../pkg/testutil/parts/parts.go) makes from any DataSet;
- an unsorted series, several results, and empty and nil results and series;
- a value that can't be written, where both must fail and write nothing.

Benchmark the old and new writers side by side on a realistic shape, such as 100 series of 1,000 points.

## The Cache Codec

`AppendDataSet` encodes a DataSet as msgpack for the byte caches, with each Segment's epochs and cells aligned in the buffer. `ReadDataSet`, which `UnmarshalDataSet` calls, decodes it without copying: the rows share the buffer, so the buffer must not change afterward. Entries written before the columnar layout fail to decode, and the caller refetches them.
