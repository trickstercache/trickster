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
	"fmt"
	"io"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	csvComma = ','
	csvQuote = '"'
)

var (
	// ErrCSVBareQuote indicates a quote in a CSV field that isn't quoted.
	ErrCSVBareQuote = fmt.Errorf("%w: %w", timeseries.ErrInvalidBody, csv.ErrBareQuote)
	// ErrCSVQuote indicates a quoted CSV field that isn't closed, or has text after its closing quote.
	ErrCSVQuote = fmt.Errorf("%w: %w", timeseries.ErrInvalidBody, csv.ErrQuote)
	// ErrCSVFieldCount indicates a CSV record with a different number of fields than the first.
	ErrCSVFieldCount = fmt.Errorf("%w: %w", timeseries.ErrInvalidBody, csv.ErrFieldCount)
)

// CSV is a Decoder for comma-separated values that splits records as encoding/csv's Reader does with
// its defaults: a quoted field may hold commas, doubled quotes and line breaks, "\r\n" ends a line
// as "\n" does, and empty lines are skipped. A record without quotes is passed on without a copy.
type CSV struct {
	lines    *Lines
	onRecord func(fields [][]byte) error
	finish   FinishFunc
	// the fields each record must have: 0 for as many as the first, or -1 for any number
	perRecord int
	fields    [][]byte
	// a record that has quoted fields, unquoted, and where each of its fields ends
	text []byte
	ends []int
	// whether a quoted field continues on the next line
	open bool
}

var _ Decoder = (*CSV)(nil)

// NewCSV returns a CSV decoder. onRecord must not retain fields or their bytes after returning, and
// finish is called by Finish after the last record.
func NewCSV(onRecord func(fields [][]byte) error, finish FinishFunc) *CSV {
	c := &CSV{onRecord: onRecord, finish: finish}
	c.lines = NewLines(c.line, c.end)
	return c
}

// SetFieldsPerRecord sets how many fields each record must have, as csv.Reader's FieldsPerRecord
// does: 0, the default, requires as many as the first record has, and a negative n allows any number.
func (c *CSV) SetFieldsPerRecord(n int) *CSV {
	c.perRecord = n
	return c
}

// SetMaxLineBytes sets the longest line, as Lines.SetMaxLineBytes does.
func (c *CSV) SetMaxLineBytes(n int) *CSV {
	c.lines.SetMaxLineBytes(n)
	return c
}

// Write passes each complete record in p to the callback.
func (c *CSV) Write(p []byte) (int, error) {
	return c.lines.Write(p)
}

// ReadFrom reads r to EOF, passing each complete record to the callback.
func (c *CSV) ReadFrom(r io.Reader) (int64, error) {
	return c.lines.ReadFrom(r)
}

// Finish passes any final record to the callback, then calls the finish function.
func (c *CSV) Finish() (timeseries.Timeseries, error) {
	return c.lines.Finish()
}

func (c *CSV) end() (timeseries.Timeseries, error) {
	if c.open {
		return nil, ErrCSVQuote
	}
	return c.finish()
}

func (c *CSV) line(line []byte) error {
	if c.open {
		// the line break is part of the quoted field the last line left open
		c.text = append(c.text, '\n')
		return c.parse(line, true)
	}
	if len(line) == 0 {
		return nil
	}
	if bytes.IndexByte(line, csvQuote) < 0 {
		c.fields = SplitFields(line, csvComma, c.fields)
		return c.record()
	}
	c.text, c.ends = c.text[:0], c.ends[:0]
	return c.parse(line, false)
}

// parse reads fields from line into text, continuing a quoted field when within is set
func (c *CSV) parse(line []byte, within bool) error {
	c.open = false
	for {
		if !within {
			if len(line) == 0 || line[0] != csvQuote {
				i := bytes.IndexByte(line, csvComma)
				field := line
				if i >= 0 {
					field = line[:i]
				}
				if bytes.IndexByte(field, csvQuote) >= 0 {
					return ErrCSVBareQuote
				}
				c.text = append(c.text, field...)
				c.ends = append(c.ends, len(c.text))
				if i < 0 {
					return c.quotedRecord()
				}
				line = line[i+1:]
				continue
			}
			line = line[1:]
		}
		within = false
		for {
			i := bytes.IndexByte(line, csvQuote)
			if i < 0 {
				c.text = append(c.text, line...)
				c.open = true
				return nil
			}
			c.text = append(c.text, line[:i]...)
			line = line[i+1:]
			if len(line) > 0 && line[0] == csvQuote {
				c.text = append(c.text, csvQuote)
				line = line[1:]
				continue
			}
			break
		}
		c.ends = append(c.ends, len(c.text))
		switch {
		case len(line) == 0:
			return c.quotedRecord()
		case line[0] != csvComma:
			return ErrCSVQuote
		}
		line = line[1:]
	}
}

func (c *CSV) quotedRecord() error {
	c.fields = c.fields[:0]
	start := 0
	for _, end := range c.ends {
		c.fields = append(c.fields, c.text[start:end])
		start = end
	}
	return c.record()
}

func (c *CSV) record() error {
	switch {
	case c.perRecord == 0:
		c.perRecord = len(c.fields)
	case c.perRecord > 0 && len(c.fields) != c.perRecord:
		return ErrCSVFieldCount
	}
	return c.onRecord(c.fields)
}
