/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package stream

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func sumWalk(sum *int64) func(*json.Decoder) error {
	// sums the numbers in {"n":[...]}, skipping any other keys
	return func(dec *json.Decoder) error {
		return Object(dec, func(key string) error {
			if key != "n" {
				return Skip(dec)
			}
			return Array(dec, func() error {
				var n json.Number
				if err := dec.Decode(&n); err != nil {
					return err
				}
				v, err := n.Int64()
				*sum += v
				return err
			})
		})
	}
}

func TestJSONFeeds(t *testing.T) {
	const body = ` {"x":{"y":[1,{"z":null}]},"n":[1,2,3],"s":"str"} `
	var sum int64
	j := NewJSON(sumWalk(&sum), finishEmpty)
	n, err := j.Write([]byte(body[:5]))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	_, err = j.Write([]byte(body[5:]))
	require.NoError(t, err)
	require.Zero(t, sum)
	_, err = j.Finish()
	require.NoError(t, err)
	require.Equal(t, int64(6), sum)

	sum = 0
	j = NewJSON(sumWalk(&sum), finishEmpty)
	_, err = j.Write([]byte(body[:10]))
	require.NoError(t, err)
	read, err := j.ReadFrom(iotest.OneByteReader(strings.NewReader(body[10:])))
	require.NoError(t, err)
	require.Equal(t, int64(len(body)-10), read)
	require.Equal(t, int64(6), sum)
	_, err = j.Write([]byte(" "))
	require.ErrorIs(t, err, ErrInputConsumed)
	_, err = j.ReadFrom(strings.NewReader(" "))
	require.ErrorIs(t, err, ErrInputConsumed)
	_, err = j.Finish()
	require.NoError(t, err)
	_, err = j.Finish()
	require.ErrorIs(t, err, ErrFinished)
	_, err = j.Write(nil)
	require.ErrorIs(t, err, ErrFinished)
}

func TestJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{"empty", "", io.ErrUnexpectedEOF},
		{"truncated", `{"n":[1,`, nil},
		{"truncated value", `{"n":[1`, nil},
		{"trailing", `{"n":[1]} 2`, ErrTrailingData},
		{"null object", `null`, ErrNull},
		{"null array", `{"n":null}`, ErrNull},
		{"wrong object", `[1]`, ErrUnexpectedToken},
		{"wrong array", `{"n":{}}`, ErrUnexpectedToken},
		{"number", `{"n":[1.5]}`, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sum int64
			j := NewJSON(sumWalk(&sum), finishEmpty)
			_, err := j.ReadFrom(strings.NewReader(test.body))
			requireError(t, err, test.err)
			_, err2 := j.Finish()
			require.Equal(t, err, err2)

			j = NewJSON(sumWalk(&sum), finishEmpty)
			_, err = j.Write([]byte(test.body))
			require.NoError(t, err)
			_, err = j.Finish()
			requireError(t, err, test.err)
			_, err = j.Write(nil)
			require.ErrorIs(t, err, ErrFinished)
		})
	}
	require.ErrorIs(t, ErrNull, timeseries.ErrInvalidBody)
	require.ErrorIs(t, ErrTrailingData, timeseries.ErrInvalidBody)
}

func requireError(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		require.Error(t, err)
		return
	}
	require.ErrorIs(t, err, want)
}

func TestJSONStickyError(t *testing.T) {
	var sum int64
	j := NewJSON(sumWalk(&sum), finishEmpty)
	_, err := j.ReadFrom(strings.NewReader(`[`))
	require.ErrorIs(t, err, ErrUnexpectedToken)
	_, err = j.Write(nil)
	require.ErrorIs(t, err, ErrUnexpectedToken)
	_, err = j.ReadFrom(strings.NewReader(`{}`))
	require.ErrorIs(t, err, ErrUnexpectedToken)
}

func TestJSONVisitorMisuse(t *testing.T) {
	walks := map[string]func(*json.Decoder) error{
		"object value not consumed": func(dec *json.Decoder) error {
			return Object(dec, func(string) error { return nil })
		},
		"array element not consumed": func(dec *json.Decoder) error {
			return Object(dec, func(string) error {
				return Array(dec, func() error { return nil })
			})
		},
	}
	for name, walk := range walks {
		t.Run(name, func(t *testing.T) {
			j := NewJSON(walk, finishEmpty)
			_, err := j.ReadFrom(strings.NewReader(`{"a":[1]}`))
			require.ErrorIs(t, err, ErrValueNotConsumed)
		})
	}
	// a visitor that consumes only part of a value leaves the walk misaligned
	partial := func(dec *json.Decoder) error {
		return Object(dec, func(string) error {
			return Array(dec, func() error {
				_, err := dec.Token()
				return err
			})
		})
	}
	for _, body := range []string{`{"a":[{"b":1}]}`, `{"a":[[1,2]]}`} {
		j := NewJSON(partial, finishEmpty)
		_, err := j.ReadFrom(strings.NewReader(body))
		require.ErrorIs(t, err, ErrUnexpectedToken, body)
	}
	keyed := func(dec *json.Decoder) error {
		return Object(dec, func(string) error {
			_, err := dec.Token()
			return err
		})
	}
	j := NewJSON(keyed, finishEmpty)
	_, err := j.ReadFrom(strings.NewReader(`{"a":[1,2]}`))
	require.ErrorIs(t, err, ErrUnexpectedToken)
}

func TestJSONNestedErrors(t *testing.T) {
	walk := func(dec *json.Decoder) error {
		return Object(dec, func(string) error {
			return Array(dec, func() error { return Skip(dec) })
		})
	}
	for _, body := range []string{`{"a":[1,}`, `{"a":[1}`, `{"a":[1] "b"}`, `{`} {
		j := NewJSON(walk, finishEmpty)
		_, err := j.ReadFrom(strings.NewReader(body))
		require.Error(t, err, body)
	}
}

func TestJSONTagString(t *testing.T) {
	var fd timeseries.FieldDefinition
	require.Equal(t, "a", JSONTagString(fd, []byte(`"a"`)))
	require.Equal(t, `a"b`, JSONTagString(fd, []byte(`"a\"b"`)))
	require.Equal(t, "é", JSONTagString(fd, []byte(`"é"`)))
	require.Equal(t, "12.5", JSONTagString(fd, []byte(`12.5`)))
	require.Equal(t, "null", JSONTagString(fd, []byte(`null`)))
	require.Equal(t, `"\x"`, JSONTagString(fd, []byte(`"\x"`)))
	require.Equal(t, `"`, JSONTagString(fd, []byte(`"`)))
}

type windowReader struct {
	r    io.Reader
	dec  *json.Decoder
	read int64
	peak int64
}

func (w *windowReader) Read(p []byte) (int, error) {
	// bytes read but not yet consumed are what the decoder is holding
	if w.dec != nil {
		w.peak = max(w.peak, w.read-w.dec.InputOffset())
	}
	n, err := w.r.Read(p[:min(len(p), 512)])
	w.read += int64(n)
	return n, err
}

func skipPeak(t *testing.T, body string, skip func(*json.Decoder) error) int64 {
	t.Helper()
	w := &windowReader{r: strings.NewReader(body)}
	var sum int64
	walk := func(dec *json.Decoder) error {
		w.dec = dec
		return Object(dec, func(key string) error {
			if key != "n" {
				return skip(dec)
			}
			return Array(dec, func() error {
				var n int64
				err := dec.Decode(&n)
				sum += n
				return err
			})
		})
	}
	_, err := NewJSON(walk, finishEmpty).ReadFrom(w)
	require.NoError(t, err)
	require.Equal(t, int64(3), sum)
	return w.peak
}

func TestSkipDoesNotHoldValue(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"skip":[`)
	for i := range 50000 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"k":"value","n":[1,2,3]}`)
	}
	sb.WriteString(`],"n":[1,2]}`)
	body := sb.String()
	require.Less(t, skipPeak(t, body, Skip), int64(64<<10))
	// decoding the value whole holds nearly all of it, which shows the measurement works
	whole := func(dec *json.Decoder) error { return dec.Decode(new(json.RawMessage)) }
	require.Greater(t, skipPeak(t, body, whole), int64(len(body)/2))
}

func TestSkip(t *testing.T) {
	tests := []struct {
		body string
		err  error
	}{
		{`"str"`, nil},
		{`12.5`, nil},
		{`null`, nil},
		{`{"a":{"b":[1,{"c":null}]},"d":"e"}`, nil},
		{`[[],{},[[{}]]]`, nil},
		{`[1,[2`, io.ErrUnexpectedEOF},
	}
	for _, test := range tests {
		j := NewJSON(Skip, finishEmpty)
		_, err := j.ReadFrom(strings.NewReader(test.body))
		if test.err == nil {
			require.NoError(t, err, test.body)
			continue
		}
		require.Error(t, err, test.body)
	}
	// with no value left to skip, Skip meets the closing delimiter instead
	closing := func(dec *json.Decoder) error {
		if _, err := dec.Token(); err != nil {
			return err
		}
		return Skip(dec)
	}
	_, err := NewJSON(closing, finishEmpty).ReadFrom(strings.NewReader(`[]`))
	require.ErrorIs(t, err, ErrUnexpectedToken)
}
