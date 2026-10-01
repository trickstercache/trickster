# Streaming Unmarshalers

A time series backend's [Modeler](../../pkg/timeseries/modeler.go) turns each upstream response body into Trickster's Common Time Series Format, the [`dataset.DataSet`](../../pkg/timeseries/dataset/dataset.go). Most providers do this in two steps. They unmarshal the whole body into a provider-specific model, such as a set of JSON structs, and then copy that model into a DataSet. That holds two full copies of the data in memory at once and allocates heavily along the way.

The packages described here let a provider decode a response in one pass, straight into a DataSet, with no intermediate model. Rows and points may arrive in any order, so a provider never needs to rewrite upstream queries (for example, by adding `ORDER BY`) to make decoding work.

| Package | Provides |
| --- | --- |
| [`pkg/timeseries/dataset`](../../pkg/timeseries/dataset/builder.go) | `Builder`, which assembles a DataSet from rows or points in any order |
| [`pkg/timeseries/dataset/stream`](../../pkg/timeseries/dataset/stream/stream.go) | the `Decoder` interface, line, CSV and JSON decoders, raw JSON scanners, value parsers, and Modeler adapters |
| [`pkg/timeseries/dataset/stream/streamtest`](../../pkg/timeseries/dataset/stream/streamtest/streamtest.go) | conformance checks and benchmarks for decoders |
| [`pkg/timeseries/epoch`](../../pkg/timeseries/epoch/parse.go) | `ParseDecimal`, exact parsing of numeric timestamps, and allocation-free parsing and formatting of RFC 3339 times |

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

The Delta Proxy Cache gives each origin fetch's `200` body to the provider's `WireUnmarshalerReader` as it arrives, decompressed and bounded by `max_object_size_bytes`, so decoding overlaps the network and the body is never held whole. A read that fails or passes the size limit fails the fetch, whatever the decoder returned, and nothing from it is cached. The engine reads to the end whatever the decoder leaves, so a decoder can stop at an error without draining its input.

## Choosing a Decoder

### Newline-Delimited Formats

`stream.NewLines(onLine, finish)` handles formats with one record per line, such as TSV. It calls `onLine` for each line without its `\n` or `\r\n` terminator, even when the line was split across `Write` calls, and delivers a final unterminated line during `Finish`. The line is only valid during the call. Lines longer than 16 MiB fail with `ErrLineTooLong` before they are buffered, so an overlong line cannot grow memory; `SetMaxLineBytes` changes the limit.

`stream.SplitFields(line, sep, dst)` splits a line into fields without allocating, reusing `dst`. It does not interpret quotes or escapes, so unescape fields yourself where the format requires it.

### CSV

`stream.NewCSV(onRecord, finish)` splits records as `encoding/csv`'s `Reader` does with its defaults: a quoted field may hold commas, doubled quotes and line breaks, `\r\n` ends a line as `\n` does, and empty lines are skipped. `onRecord` gets each record's fields, which are valid only during the call. A record without quotes is passed on as slices of the input, so it costs no copy or allocation; a record with quoted fields is unquoted into a buffer the decoder reuses.

- `SetFieldsPerRecord(n)` works as the `Reader`'s `FieldsPerRecord` does: `0`, the default, requires every record to have as many fields as the first, and a negative `n` allows any number.
- A quote in a field that isn't quoted fails with `ErrCSVBareQuote`, a quoted field that is never closed or has text after its closing quote fails with `ErrCSVQuote`, and a record of the wrong width fails with `ErrCSVFieldCount`. All three wrap `timeseries.ErrInvalidBody`.
- Lines are limited as `Lines` limits them, per line of a quoted field that spans several.

### JSON Documents

`stream.NewJSON(walk, finish)` handles a single JSON document. The `walk` function receives an `encoding/json/jsontext` `Decoder` and must consume exactly one value from it. The decoder accepts a repeated object key and invalid UTF-8, as `encoding/json` does. Only whitespace may follow that value: a second JSON value fails with `ErrTrailingData`, and anything else fails as a syntax error. These helpers let a walk hold only the current token or value in memory:

- `stream.Object(dec, func(key string) error)` calls the function for each key, in the order the keys arrive.
- `stream.ObjectBytes(dec, func(key []byte) error)` is `Object` with each key as bytes, valid until the function reads from `dec`. Compare or copy the key first; a key compared in a `switch string(key)` costs no allocation.
- `stream.Array(dec, func() error)` calls the function once for each element.
- `stream.Skip(dec)` consumes and discards the next value without holding it, so a large skipped value is never in memory.
- `stream.Decode(dec, &v, opts...)` decodes the next value into `v` with `encoding/json`'s semantics. Options such as `jsonv2.RejectUnknownMembers(true)` apply on top. Use it for small structures, like an envelope or a schema.
- `stream.AppendString(dst, raw)` appends the text of a raw JSON string, unescaping it only when it has escapes.
- `stream.StringText(raw, &buf)` returns the text of a raw JSON string: `raw`'s own bytes when nothing in it is escaped, or else the text decoded into `buf`.
- `stream.FieldName(key, names...)` matches a key against field names as `encoding/json` matches struct fields, exactly and then ignoring case.
- `stream.Interner` converts bytes to strings, sharing one string for each name, and each short value, that a response repeats.

Each callback must consume exactly the value it was called for, for example with `dec.ReadValue`, `Decode`, a nested `Object` or `Array`, or `Skip`:

- A callback that consumes nothing fails with `ErrValueNotConsumed`.
- One that consumes only part of a value fails with `ErrUnexpectedToken`.
- `Object` and `Array` return `ErrNull` for a JSON `null` after consuming it, so a caller that accepts a null can check with `errors.Is` and carry on.
- They return `ErrUnexpectedToken` when the value is the wrong kind.

Read row values with `dec.ReadValue()`, one element at a time. It returns the value's raw bytes without allocating, and they're valid only until the next read, so pass them straight to the Builder's adders or `SetTag`, which copy what they keep. Reading a small row whole with one `ReadValue`, such as a `[time, value]` pair or a row object, is faster than reading it token by token. The decoder has already validated those bytes, so `stream.ArrayElements(raw)` and `stream.ObjectMembers(raw)` can walk them without checking the grammar again. The Prometheus and InfluxQL decoders read each row array this way, and the InfluxDB 3 SQL decoder each row object.

JSON does not guarantee key order. If something you need first, such as a schema, might arrive after the data that depends on it, copy the early data's raw bytes into a buffer the decoder reuses, and walk them with `stream.NewJSONDecoder(bytes.NewReader(buf))` once the schema has been read. The InfluxQL decoder does this for a series' values that arrive before its columns.

A JSON Lines body is a sequence of JSON values. A walk given to `NewJSON` reads it by calling `dec.ReadValue` until it returns `io.EOF`.

### Several Formats in One Endpoint

`stream.Sniff(pick)` returns a Decoder for a body whose format its first byte tells. `pick` gets that byte and returns the Decoder to give the whole body to. An empty body fails at `Finish` with `timeseries.ErrInvalidBody`. The InfluxDB 3 SQL decoder uses it to tell a JSON array, JSON Lines and CSV apart.

### Other Formats

A format that fits neither decoder can implement `stream.Decoder` directly. The conformance checks feed a decoder with `Write` calls, with a single `ReadFrom` call, or with `Write` calls followed by one `ReadFrom`, and expect the same result each way. Errors should be sticky, and `Finish` is called once.

The ClickHouse Native decoder (`clickhouse/model/decoder_native.go`) is an example for a binary format of self-contained blocks:
- It buffers its input and reads each block once all of it has arrived, then drops it. Only the block being received is held, not the whole body.
- A block that hasn't all arrived is read again only once the buffer has doubled, so the reads it repeats cost at most as much as the body itself.
- `ReadFrom` reads straight into the buffer, and the buffer and the block's column storage are pooled between decodes.
- Each column is read whole into typed storage, and the rows are then added to a row-mode Builder.

## Building the DataSet

`dataset.NewBuilder(trq, opts)` returns a Builder for one response. `BuilderOptions` sets:

- `Fields`: the timestamp, tag and value fields of each row.
- `SeriesName` and `QueryStatement`: copied into each series header the Builder creates. `NameSeries`, when set, names each new series from its tags instead, once per series.
- `AddValueField`, in row mode, adds a value field after rows were committed. A format that leaves null values out, as InfluxDB 3's JSON does, uses it to add a column it first sees on a later row. Series created earlier are widened, and their earlier rows hold null in the new column.
- `Duplicates`: what to do with points in one series that share an epoch: `DuplicatesKeep`, `DuplicatesFirstWins`, `DuplicatesLastWins` or `DuplicatesError`.
- `SortSeries`: sorts each result's series by their tags when the build finishes.
- `TagString`: converts a tag's raw bytes to its value in the series' `Tags`. By default the bytes are used as they are; `stream.JSONTagString` unquotes JSON strings.

### Row Mode

Use row mode for formats that send one row per point, such as SQL results and TSV or CSV:

```go
r := b.Row()      // reused, and valid until the next call to Row
r.SetEpoch(ep)
r.SetTag(0, host) // an index into BuilderOptions.Fields.Tags; the bytes are copied
r.AddFloat64(v)   // values in BuilderOptions.Fields.Values order
if err := r.Commit(); err != nil {
	return err
}
```

Each value is added with the adder for its type, so it is written straight into its column without being boxed:

- `AddFloat64`, `AddInt64`, `AddUint64`, `AddBool` and `AddNull`;
- `AddString(raw)` for text and `AddBytes(raw)` for binary values, both copying `raw`;
- `AddNumber(raw)` for a number kept as its literal text, like a `json.Number`.

`AddValue(v)` takes a value that is already boxed, like one `stream.ParseValue` returns, and picks the adder by its Go type. When a decoder knows a value's type as it reads it, the typed adder saves boxing it.

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

Rows committed while a series is open go to that series and may not set tags. `StartSeries` reopens the series with an identical header if there is one, so a series that arrives in pieces becomes one series. `StartNewSeries` opens a new series even when one has an identical header, for a format such as Graphite's, where a response that lists one series twice holds two. `AppendPoint` adds a `Point` you have already built, adding its values with `AddValue`. For formats that return several statements, `SetResult(statementID, name)` sends later rows and series to another result, creating it if needed.

### Finishing

The Builder matches series the same way merges do: the header hash finds candidates, and a comparison of the headers confirms the match. So the DataSet never holds two series that a later merge would treat as one, and two different series whose hashes collide stay separate, both in the Builder and in later merges.

`Finish` returns the DataSet. It sorts only the series whose points arrived out of order, using a stable sort that keeps arrival order among equal epochs, and then applies the duplicate policy. When a series' points do arrive in order, duplicates are handled as they arrive, so `DuplicatesError` fails the `Commit` immediately. `Finish` also calculates each series header's size, and sets the DataSet's `TimeRangeQuery` and `ExtentList` from the query.

The Builder logs rows as they arrive and lays them out by column when it finishes: each series holds its epochs in one array and each value column in another, all cut from a few arrays the whole DataSet shares, with text and binary values in one shared byte array. So a row does not need an allocation of its own, and a series' size is the size of its arrays. Read the columns in place through `Series.Segments()` or `Result.Rows`; [Columnar DataSets](./columnar-datasets.md) covers reading them, and writing a response from them.

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
- `epoch.ParseRFC3339(raw, layout)` parses a time as `time.Parse` does with `time.RFC3339` or `time.RFC3339Nano`. `epoch.ParseCanonicalTime(raw, zoned)` parses only the canonical UTC form, `YYYY-MM-DDTHH:MM:SS` with an optional fraction, followed by `Z` when `zoned` and by nothing when not, and reports whether it did. Neither allocates for a canonical time, and they return exactly what `time.Parse` would.
- `epoch.AppendCanonicalTime(dst, e, fraction, zoned)` is the inverse for renderers: it writes what `time.Time.AppendFormat` writes with `time.RFC3339`, `time.RFC3339Nano`, or `time.RFC3339Nano` without its zone. It is as fast as Go's own formatting of the two RFC 3339 layouts, and more than three times faster for the zone-less one, which Go formats through its general layout engine.
- `epoch.ParseSQLDateTime(raw)` and `epoch.ParseSQLDate(raw)` parse `YYYY-MM-DD HH:MM:SS`, with an optional fraction after a period, and `YYYY-MM-DD`. They return exactly what `time.Parse` returns in UTC with `2006-01-02 15:04:05.999999999` and `2006-01-02`, and report `false` for any other text so a caller can fall back. Neither allocates.
- `Epoch.AppendFormat` writes the SQL date and time layouts without building a `time.Time`, more than three times faster than `time.Time.AppendFormat`, and RFC 3339 with `AppendCanonicalTime`.
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
- `Unwrap`: extracts the DataSet from a provider's wrapper type. It applies to the legacy result too, so it can also normalize a difference the new decoder makes on purpose; the InfluxQL tests use it to compare numbers the old decoder always made float64s.

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
3. Add a test that decodes from a buffer, overwrites the buffer, and checks the result is unchanged. Callers reuse and release their input buffers once a decode returns, so a decoder must never keep references into its input.
4. Compare the old and new decoders with `streamtest.Bench`.
5. Point the Modeler's wire unmarshalers at the adapters, and move the old decoder into a `_test.go` file as the `Legacy` oracle.

Formats that send each series as one block, with its points in time order, map directly onto series mode; examples are Prometheus, InfluxQL JSON and Graphite. Flux CSV uses series mode too: each of its tables is one series, and each can have a different schema, which row mode's fixed fields can't follow. Formats that send rows in no particular order use row mode and rely on the Builder to sort when needed; examples are InfluxDB 3 SQL, ClickHouse and Druid. ClickHouse's series interleave row by row, as a `GROUP BY` bucket holds one row per series, so it uses row mode with `NameSeries` rather than reopening a series for every row. The MySQL provider's wire-protocol path never builds a DataSet, so it is not a candidate.
