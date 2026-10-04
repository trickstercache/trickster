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
	"errors"
	"io"
	"testing"
	"testing/iotest"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// a Sniff decoder whose inner decoder reports the first byte and every line it was given
func newSniffTest(picked *byte, lines *[]string) Decoder {
	return Sniff(func(first byte) Decoder {
		*picked = first
		return NewLines(func(line []byte) error {
			*lines = append(*lines, string(line))
			return nil
		}, func() (timeseries.Timeseries, error) { return &dataset.DataSet{}, nil })
	})
}

func TestSniff(t *testing.T) {
	feeds := map[string]func(Decoder, []byte) error{
		"write": func(d Decoder, b []byte) error {
			if _, err := d.Write(nil); err != nil {
				return err
			}
			_, err := d.Write(b)
			return err
		},
		"readfrom": func(d Decoder, b []byte) error {
			_, err := d.ReadFrom(iotest.OneByteReader(bytes.NewReader(b)))
			return err
		},
		"write-then-readfrom": func(d Decoder, b []byte) error {
			if _, err := d.Write(b[:1]); err != nil {
				return err
			}
			_, err := d.ReadFrom(bytes.NewReader(b[1:]))
			return err
		},
	}
	for name, feed := range feeds {
		var picked byte
		var lines []string
		d := newSniffTest(&picked, &lines)
		if err := feed(d, []byte("[a\nb\n")); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := d.Finish(); err != nil {
			t.Fatalf("%s: Finish: %v", name, err)
		}
		if picked != '[' || len(lines) != 2 || lines[0] != "[a" || lines[1] != "b" {
			t.Errorf("%s: picked %q, lines %q", name, picked, lines)
		}
		if _, err := d.Write([]byte("x")); !errors.Is(err, ErrFinished) {
			t.Errorf("%s: Write after Finish: got %v", name, err)
		}
		if _, err := d.ReadFrom(bytes.NewReader(nil)); !errors.Is(err, ErrFinished) {
			t.Errorf("%s: ReadFrom after Finish: got %v", name, err)
		}
		if _, err := d.Finish(); !errors.Is(err, ErrFinished) {
			t.Errorf("%s: second Finish: got %v", name, err)
		}
	}
}

func TestSniffWithoutInput(t *testing.T) {
	var picked byte
	var lines []string
	d := newSniffTest(&picked, &lines)
	if _, err := d.ReadFrom(bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Finish(); !errors.Is(err, timeseries.ErrInvalidBody) {
		t.Errorf("got %v, want ErrInvalidBody", err)
	}
	errRead := errors.New("read failed")
	d = newSniffTest(&picked, &lines)
	if _, err := d.ReadFrom(iotest.ErrReader(errRead)); !errors.Is(err, errRead) {
		t.Errorf("got %v, want the read error", err)
	}
	if _, err := d.Write([]byte("x")); !errors.Is(err, errRead) {
		t.Errorf("Write after a failed read: got %v", err)
	}
	if _, err := d.Finish(); !errors.Is(err, errRead) {
		t.Errorf("Finish after a failed read: got %v", err)
	}
	// the inner decoder's error on the first byte stops the read
	d = Sniff(func(byte) Decoder {
		return NewLines(func([]byte) error { return errRead }, nil).SetMaxLineBytes(0)
	})
	if _, err := d.ReadFrom(io.MultiReader(bytes.NewReader([]byte("\n")))); !errors.Is(err, errRead) {
		t.Errorf("got %v, want the inner decoder's error", err)
	}
}
