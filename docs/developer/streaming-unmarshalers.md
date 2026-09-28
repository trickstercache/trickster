# Streaming Unmarshalers

A time series backend's [Modeler](../../pkg/timeseries/modeler.go) turns each upstream response body into Trickster's Common Time Series Format, the [`dataset.DataSet`](../../pkg/timeseries/dataset/dataset.go). Most providers do this in two steps. They unmarshal the whole body into a provider-specific model, such as a set of JSON structs, and then copy that model into a DataSet. That holds two full copies of the data in memory at once and allocates heavily along the way.

The packages described here let a provider decode a response in one pass, straight into a DataSet, with no intermediate model. Rows and points may arrive in any order, so a provider never needs to rewrite upstream queries (for example, by adding `ORDER BY`) to make decoding work.

| Package | Provides |
| --- | --- |
| [`pkg/timeseries/dataset`](../../pkg/timeseries/dataset/builder.go) | `Builder`, which assembles a DataSet from rows or points in any order |
| [`pkg/timeseries/dataset/stream`](../../pkg/timeseries/dataset/stream/stream.go) | the `Decoder` interface, line and JSON decoders, value parsers, and Modeler adapters |
| [`pkg/timeseries/dataset/stream/streamtest`](../../pkg/timeseries/dataset/stream/streamtest/streamtest.go) | conformance checks and benchmarks for decoders |
| [`pkg/timeseries/epoch`](../../pkg/timeseries/epoch/parse.go) | `ParseDecimal`, exact parsing of numeric timestamps |

## How It Fits Together

A provider writes a `stream.NewDecoderFunc`. Given the request's `TimeRangeQuery`, it returns a `stream.Decoder` for one response. A `Decoder` is an `io.Writer` and an `io.ReaderFrom`, plus a `Finish` method that returns the decoded `timeseries.Timeseries`. The stream package's adapters turn that function into the Modeler's wire unmarshalers:

```go
func NewModeler() *timeseries.Modeler {
	return timeseries.NewModeler(
		stream.BytesUnmarshaler(newDecoder), stream.ReaderUnmarshaler(newDecoder),
		MarshalTimeseries, MarshalTimeseriesWriter,
		dataset.UnmarshalDataSet, dataset.MarshalDataSet)
}
```

`ReaderUnmarshaler` passes the response body to the decoder's `ReadFrom`, so decoding happens while the body is read. If you feed a decoder yourself, call `ReadFrom` instead of using `io.Copy`. When the source is a `bytes.Reader`, `io.Copy` uses the reader's `WriteTo`, which delivers the whole body in a single `Write`, and the JSON decoder then has to buffer all of it before it can start.

Today the proxy engine reads each upstream body into memory before calling the unmarshaler, so the current savings come from skipping the intermediate model. Once the engine passes response bodies through directly, the same decoders will read from the network with no changes.

## Choosing a Decoder

### Newline-Delimited Formats

`stream.NewLines(onLine, finish)` handles formats with one record per line, such as TSV, CSV without quoted newlines, and JSON Lines. It calls `onLine` for each line without its `\n` or `\r\n` terminator, even when the line was split across `Write` calls, and delivers a final unterminated line during `Finish`. The line is only valid during the call. Lines longer than 16 MiB fail with `ErrLineTooLong` before they are buffered, so an overlong line cannot grow memory; `SetMaxLineBytes` changes the limit.

`stream.SplitFields(line, sep, dst)` splits a line into fields without allocating, reusing `dst`. It does not interpret quotes or escapes, so unescape fields yourself where the format requires it.

### JSON Documents

`stream.NewJSON(walk, finish)` handles a single JSON document. The `walk` function receives an `encoding/json` `Decoder` with `UseNumber` set, and must consume exactly one value from it. Only whitespace may follow that value: a second JSON value fails with `ErrTrailingData`, and anything else fails as a syntax error. Three helpers let a walk hold only the current token or element in memory:

- `stream.Object(dec, func(key string) error)` calls the function for each key, in the order the keys arrive.
- `stream.Array(dec, func() error)` calls the function once for each element.
- `stream.Skip(dec)` consumes and discards the next value a token at a time, so a large skipped value is never held in memory.

Each callback must consume the value it was called for, for example with `dec.Decode`, a nested `Object` or `Array`, or `Skip`. A callback that returns without doing so fails with `ErrValueNotConsumed`. `Object` and `Array` return `ErrNull` for a JSON `null` after consuming it, so a caller that accepts a null can check with `errors.Is` and carry on. They return `ErrUnexpectedToken` when the value is the wrong kind.

Decode every row into the same `[]json.RawMessage`. `encoding/json` reuses the slice and each element's buffer, so decoding rows stops allocating once those buffers have grown.

JSON does not guarantee key order. If something you need first, such as a schema, might arrive after the data that depends on it, hold the early data as a `json.RawMessage` and process it once the schema has been read.

### Other Formats

A format that fits neither decoder can implement `stream.Decoder` directly. The conformance checks feed a decoder with `Write` calls, with a single `ReadFrom` call, or with `Write` calls followed by one `ReadFrom`, and expect the same result each way. Errors should be sticky, and `Finish` is called once.

## Building the DataSet

`dataset.NewBuilder(trq, opts)` returns a Builder for one response. `BuilderOptions` sets:

- `Fields`: the timestamp, tag and value fields of each row.
- `SeriesName` and `QueryStatement`: copied into each series header the Builder creates.
- `Duplicates`: what to do with points in one series that share an epoch: `DuplicatesKeep`, `DuplicatesFirstWins`, `DuplicatesLastWins` or `DuplicatesError`.
- `SortSeries`: sorts each result's series by their tags when the build finishes.
- `TagString`: converts a tag's raw bytes to its value in the series' `Tags`. By default the bytes are used as they are; `stream.JSONTagString` unquotes JSON strings.

### Row Mode

Use row mode for formats that send one row per point, such as SQL results and TSV or CSV:

```go
r := b.Row()      // reused, and valid until the next call to Row
r.SetEpoch(ep)
r.SetTag(0, host) // an index into BuilderOptions.Fields.Tags; the bytes are copied
r.AddValue(v)     // in BuilderOptions.Fields.Values order
if err := r.Commit(); err != nil {
	return err
}
```

The Builder remembers each raw tag encoding it has seen, so a row that repeats an earlier row's tag bytes finds its series with one lookup that does not allocate. A new encoding is converted with `TagString` and matched against the existing series by header, so equivalent encodings, such as `"a"` and `"\u0061"` in JSON, share a series. A tag that is never set is left out of the series' `Tags`, so an unset tag and an empty one produce different series.

### Series Mode

Use series mode for formats that send each series as one block, such as a Prometheus matrix or InfluxQL JSON:

```go
b.StartSeries(dataset.SeriesHeader{Name: "up", Tags: tags, ValueFieldsList: valueFields})
for _, p := range points {
	r := b.Row()
	r.SetEpoch(p.epoch)
	r.AddValue(p.value)
	if err := r.Commit(); err != nil {
		return err
	}
}
b.EndSeries()
```

Rows committed while a series is open go to that series and may not set tags. `StartSeries` reopens the series with an identical header if there is one, so a series that arrives in pieces becomes one series. `AppendPoint` adds a `Point` you have already built. For formats that return several statements, `SetResult(statementID, name)` sends later rows and series to another result, creating it if needed.

### Finishing

The Builder matches series the same way merges do: the header hash finds candidates, and a comparison of the headers confirms the match. So the DataSet never holds two series that a later merge would treat as one, and two different series whose hashes collide stay separate, both in the Builder and in later merges.

`Finish` returns the DataSet. It sorts only the series whose points arrived out of order, using a stable sort that keeps arrival order among equal epochs, and then applies the duplicate policy. When a series' points do arrive in order, duplicates are handled as they arrive, so `DuplicatesError` fails the `Commit` immediately. `Finish` also calculates each series header's size, and sets the DataSet's `TimeRangeQuery` and `ExtentList` from the query.

Point values are carved from shared, chunked backing arrays, so a point does not need an allocation of its own. `dataset.PointSize` is the size estimate the Builder records for each point.

`ErrInvalidRow` and `ErrDuplicateEpoch` wrap `timeseries.ErrInvalidBody`, and `ErrBuilderFinished` reports use after `Finish`. `ErrInvalidRow` covers:

- a row without an epoch;
- a row with the wrong number of values;
- a tag index out of range;
- tags set on a series-mode row;
- `AppendPoint` with no open series.

`ErrDuplicateEpoch` reports a duplicate under `DuplicatesError`.

A provider that wraps the DataSet in its own `Timeseries` type can do so in its finish function, because a `FinishFunc` returns a `timeseries.Timeseries`.

## Parsing Values

- `epoch.ParseDecimal(raw, unit)` parses a count of seconds, milliseconds, microseconds or nanoseconds, where `unit` is `DateTimeUnixSecs`, `DateTimeUnixMilli`, `DateTimeUnixMicro` or `DateTimeUnixNano`. The count may have a fraction and an exponent, and may be wrapped in JSON quotes. It uses integer math, and rejects precision finer than one nanosecond and values outside the `int64` range.
- `stream.ParseValue(raw, dt)` parses the text of a TSV or CSV cell as the field's data type:
  - integer and Unix timestamp types return `int64`, except `Uint64`, which returns `uint64`;
  - `Float64` returns `float64`, and `Bool` returns `bool`;
  - text types, and RFC 3339 and SQL date and time types, return `string`;
  - empty text is `nil` for any type except text;
  - `Unknown` infers a `bool` or number from JSON-style literals and falls back to `string`.
- `stream.ParseJSONValue(raw, dt)` parses a raw JSON value. `null` is `nil`, and quoted values are unquoted first, so numbers that an API sends as strings still parse as numbers.

Parse errors wrap `stream.ErrInvalidValue`, which wraps `timeseries.ErrInvalidBody`.

## Testing a Decoder

`streamtest.Conformance(t, newDecoder, c)` feeds `c.Body` to the decoder every way it can be fed:
- in one `Write`, one byte at a time, and in random chunks;
- through `ReadFrom` with several reader behaviors;
- as `Write` calls followed by `ReadFrom`;
- through both adapters.

It reports each result that differs, and checks that a failed read surfaces as an error instead of a partial result. The `Case` adds optional checks:

- `WantErr`: the error every feed must return, matched with `errors.Is`. `streamtest.ErrAny` accepts any error.
- `Want`: the DataSet every feed must produce, ignoring sizes.
- `Legacy`: an existing unmarshaler whose DataSet must match, ignoring sizes.
- `Shuffle`: reorders the body without changing its meaning; the result must match apart from series order. `streamtest.ShuffleLines(n)` shuffles every line after the first `n`.
- `Unwrap`: extracts the DataSet from a provider's wrapper type.

`streamtest.Compare(want, got, opts)` reports the first difference between two DataSets, for your own assertions. `streamtest.Bench` measures an unmarshaler the way the proxy engine calls it, so an old and a new decoder can be compared side by side:

```go
b.Run("legacy", func(b *testing.B) { streamtest.Bench(b, model.UnmarshalTimeseriesReader, trq, body) })
b.Run("stream", func(b *testing.B) { streamtest.Bench(b, stream.ReaderUnmarshaler(newDecoder), trq, body) })
```

Randomness in these tests comes from `pkg/util/weak/weaktest`; see [Non-Cryptographic Randomness](./weak-randomness.md).

## A Complete Decoder

This decoder reads `time`, `host` and `value` columns from tab-separated rows that may arrive in any order:

```go
var fields = timeseries.SeriesFields{
	Timestamp: timeseries.FieldDefinition{Name: "time", DataType: timeseries.DateTimeUnixMilli,
		Role: timeseries.RoleTimestamp},
	Tags: timeseries.FieldDefinitions{{Name: "host", DataType: timeseries.String,
		Role: timeseries.RoleTag, OutputPosition: 1}},
	Values: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.Float64,
		Role: timeseries.RoleValue, OutputPosition: 2}},
}

func newTSVDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	b := dataset.NewBuilder(trq, dataset.BuilderOptions{
		Fields: fields, SeriesName: "tsv", Duplicates: dataset.DuplicatesError,
	})
	var header bool
	var cols [][]byte
	onLine := func(line []byte) error {
		if !header {
			header = true // the first line names the columns
			return nil
		}
		cols = stream.SplitFields(line, '\t', cols)
		if len(cols) != 3 {
			return timeseries.ErrInvalidBody
		}
		ep, err := epoch.ParseDecimal(cols[0], timeseries.DateTimeUnixMilli)
		if err != nil {
			return err
		}
		v, err := stream.ParseValue(cols[2], timeseries.Float64)
		if err != nil {
			return err
		}
		r := b.Row()
		r.SetEpoch(ep)
		r.SetTag(0, cols[1])
		r.AddValue(v)
		return r.Commit()
	}
	return stream.NewLines(onLine, func() (timeseries.Timeseries, error) {
		return b.Finish()
	}), nil
}
```

[`decoders_test.go`](../../pkg/timeseries/dataset/stream/decoders_test.go) in the stream package has this decoder with header validation, along with two more to start from. `newMatrixDecoder` decodes a Prometheus-style matrix in series mode, and `newRowsDecoder` decodes JSON rows and checks a row count that arrives after them. Their tests are in [`conformance_test.go`](../../pkg/timeseries/dataset/stream/conformance_test.go).

## Converting an Existing Provider

1. Write the decoder beside the provider's current unmarshaler.
2. Run `streamtest.Conformance` over the provider's test bodies, with `Legacy` set to the current `WireUnmarshalerReader`. Sizes may differ, but everything else should match. Where the old behavior is a bug, assert the corrected result with `Want` instead, and point it out in the pull request.
3. Compare the old and new decoders with `streamtest.Bench`.
4. Point the Modeler's wire unmarshalers at the adapters, and remove the old model code.

Formats that send each series as one block, with its points in time order, map directly onto series mode; examples are Prometheus, InfluxQL JSON and Graphite. Formats that send rows in no particular order use row mode and rely on the Builder to sort when needed; examples are InfluxDB 3 SQL, ClickHouse, Flux CSV and Druid. The MySQL provider's wire-protocol path never builds a DataSet, so it is not a candidate.
