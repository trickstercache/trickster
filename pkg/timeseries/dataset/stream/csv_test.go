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
	"bytes"
	"encoding/csv"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var csvBodies = []string{
	"a,b,c\n1,2,3\n",
	"a,b,c\r\n1,2,3\r\n",
	"a,b,c\n1,2,3",
	"a,b,c\n1,2,3\r",
	"a,b\n\n\n1,2\n\r\n3,4\n",
	"a,\n,b\n,\n",
	`"a","b"` + "\n" + `"1,2","3""4"` + "\n",
	`"a` + "\n" + `b",c` + "\n",
	`"a` + "\r\n" + `b",c` + "\r\n",
	`"a` + "\n\n" + `b",c` + "\n",
	`"",""` + "\n",
	`"a",` + "\n",
	`x,"a"` + "\n",
	`a,"b` + "\n" + `c` + "\n" + `d"` + "\n",
	" a , b \n",
	"\r\r\n",
	"a\rb,c\n",
	`"a"` + "\r" + `b` + "\n",
	// a quoted field that runs to the end of the body, closed or not
	`a,"b"`,
	`a,"b`,
	`a,"b` + "\n",
	`a,"`,
	// a quote where one can't be
	`a"b,c` + "\n",
	`"a"b,c` + "\n",
	`"a" ,c` + "\n",
	`a,b"` + "\n",
	// records of differing widths
	"a,b\n1,2,3\n",
	"a,b,c\n1\n",
	"",
	"\n",
	"\n\n",
	`"`,
	`""""` + "\n",
	"#datatype,string,long\n#group,false,false\n#default,_result,\n,result,table\n,,0\n",
}

type csvOutcome struct {
	records [][]string
	err     error
}

func decodeCSV(body []byte, perRecord int, feed func(*CSV, []byte) error) csvOutcome {
	var out csvOutcome
	c := NewCSV(func(fields [][]byte) error {
		rec := make([]string, len(fields))
		for i, f := range fields {
			rec[i] = string(f)
		}
		out.records = append(out.records, rec)
		return nil
	}, func() (timeseries.Timeseries, error) { return nil, nil })
	c.SetFieldsPerRecord(perRecord)
	if out.err = feed(c, body); out.err == nil {
		_, out.err = c.Finish()
	}
	return out
}

func readCSV(body []byte, perRecord int) csvOutcome {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = perRecord
	records, err := r.ReadAll()
	return csvOutcome{records, err}
}

var csvFeeds = map[string]func(*CSV, []byte) error{
	"write": func(c *CSV, b []byte) error {
		_, err := c.Write(b)
		return err
	},
	"bytewise": func(c *CSV, b []byte) error {
		for i := range b {
			if _, err := c.Write(b[i : i+1]); err != nil {
				return err
			}
		}
		return nil
	},
	"readfrom": func(c *CSV, b []byte) error {
		_, err := c.ReadFrom(iotest.HalfReader(bytes.NewReader(b)))
		return err
	},
}

func checkCSV(t *testing.T, body []byte) {
	t.Helper()
	for _, perRecord := range []int{0, -1} {
		want := readCSV(body, perRecord)
		for name, feed := range csvFeeds {
			got := decodeCSV(body, perRecord, feed)
			if want.err != nil {
				if got.err == nil {
					t.Fatalf("%q %s (%d): got %q, want error %v", body, name, perRecord, got.records, want.err)
				}
				if !errors.Is(got.err, timeseries.ErrInvalidBody) {
					t.Fatalf("%q %s (%d): got error %v, want ErrInvalidBody", body, name, perRecord, got.err)
				}
				continue
			}
			if got.err != nil || !slices.EqualFunc(got.records, want.records, slices.Equal) {
				t.Fatalf("%q %s (%d): got %q %v, want %q", body, name, perRecord, got.records, got.err,
					want.records)
			}
		}
	}
}

func TestCSVMatchesEncodingCSV(t *testing.T) {
	for _, body := range csvBodies {
		checkCSV(t, []byte(body))
	}
}

func TestCSVErrors(t *testing.T) {
	cases := map[string]error{
		`a"b` + "\n":   ErrCSVBareQuote,
		`"a"b` + "\n":  ErrCSVQuote,
		`"a`:           ErrCSVQuote,
		"a,b\n1,2,3\n": ErrCSVFieldCount,
	}
	for body, want := range cases {
		if got := decodeCSV([]byte(body), 0, csvFeeds["write"]); !errors.Is(got.err, want) {
			t.Errorf("%q: got %v, want %v", body, got.err, want)
		}
	}
	// a callback's error stops the decode
	errStop := errors.New("stop")
	c := NewCSV(func([][]byte) error { return errStop }, nil)
	if _, err := c.Write([]byte("a\n")); !errors.Is(err, errStop) {
		t.Errorf("got %v, want the callback's error", err)
	}
	if _, err := c.Finish(); !errors.Is(err, errStop) {
		t.Errorf("Finish: got %v, want the callback's error", err)
	}
	long := NewCSV(func([][]byte) error { return nil }, nil).SetMaxLineBytes(4)
	if _, err := long.Write([]byte("abcdef\n")); !errors.Is(err, ErrLineTooLong) {
		t.Errorf("got %v, want ErrLineTooLong", err)
	}
}

func TestCSVRecordsDoNotAllocate(t *testing.T) {
	var fields int
	c := NewCSV(func(f [][]byte) error {
		fields += len(f)
		return nil
	}, nil)
	line := []byte("2026-09-30T00:00:00Z,host-1,cpu,12.5,7\n")
	quoted := []byte(`2026-09-30T00:00:00Z,"host,1",cpu,12.5,7` + "\n")
	for _, b := range [][]byte{line, quoted} {
		_, _ = c.Write(b)
		if n := testing.AllocsPerRun(100, func() { _, _ = c.Write(b) }); n != 0 {
			t.Errorf("%q: %v allocations per record", b, n)
		}
	}
}

func FuzzCSV(f *testing.F) {
	for _, body := range csvBodies {
		f.Add([]byte(body))
	}
	f.Add([]byte(strings.Repeat(`"a""b",c`+"\n", 3)))
	f.Fuzz(checkCSV)
}
