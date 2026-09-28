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
	"strings"
	"testing"
	"testing/iotest"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

var errCallback = errors.New("callback error")

func collectLines(lines *[]string) func([]byte) error {
	return func(line []byte) error {
		*lines = append(*lines, string(line))
		return nil
	}
}

func finishEmpty() (timeseries.Timeseries, error) {
	return &dataset.DataSet{}, nil
}

func TestLinesWrite(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"single", []string{"a\nb\n"}, []string{"a", "b"}},
		{"unterminated", []string{"a\nb"}, []string{"a", "b"}},
		{"crlf", []string{"a\r\nb\r\n"}, []string{"a", "b"}},
		{"split crlf", []string{"a\r", "\nb"}, []string{"a", "b"}},
		{"split lines", []string{"ab", "c\nd", "e", "\n", "f"}, []string{"abc", "de", "f"}},
		{"empty lines", []string{"\n\na\n", "\n"}, []string{"", "", "a", ""}},
		{"final cr", []string{"a\r"}, []string{"a"}},
		{"nothing", nil, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			l := NewLines(collectLines(&got), finishEmpty)
			for _, c := range test.chunks {
				n, err := l.Write([]byte(c))
				require.NoError(t, err)
				require.Equal(t, len(c), n)
			}
			ts, err := l.Finish()
			require.NoError(t, err)
			require.NotNil(t, ts)
			require.Equal(t, test.want, got)
		})
	}
}

func TestLinesMaxLineBytes(t *testing.T) {
	var got []string
	l := NewLines(collectLines(&got), finishEmpty).SetMaxLineBytes(3)
	_, err := l.Write([]byte("abc\r"))
	require.NoError(t, err)
	_, err = l.Write([]byte("\nab"))
	require.NoError(t, err)
	// only a trailing "\r" may take a partial line past the limit
	n, err := l.Write([]byte("cd"))
	require.ErrorIs(t, err, ErrLineTooLong)
	require.ErrorIs(t, err, timeseries.ErrInvalidBody)
	require.Zero(t, n)
	_, err = l.Write([]byte("x"))
	require.ErrorIs(t, err, ErrLineTooLong)
	_, err = l.Finish()
	require.ErrorIs(t, err, ErrLineTooLong)
	require.Equal(t, []string{"abc"}, got)

	l = NewLines(collectLines(&got), finishEmpty).SetMaxLineBytes(3)
	n, err = l.Write([]byte("ab\nabcd\nab\n"))
	require.ErrorIs(t, err, ErrLineTooLong)
	require.Equal(t, 3, n)

	require.Equal(t, DefaultMaxLineBytes, NewLines(nil, nil).SetMaxLineBytes(0).max)
}

func TestLinesLimitPrecedesGrowth(t *testing.T) {
	var got []string
	l := NewLines(collectLines(&got), finishEmpty).SetMaxLineBytes(8)
	_, err := l.Write([]byte("abc"))
	require.NoError(t, err)
	huge := append(bytes.Repeat([]byte{'x'}, 1<<20), '\n')
	n, err := l.Write(huge)
	require.ErrorIs(t, err, ErrLineTooLong)
	require.Zero(t, n)
	require.Less(t, cap(l.partial), 1<<10)

	// a "\r" held from one write still ends the line when its "\n" arrives
	l = NewLines(collectLines(&got), finishEmpty).SetMaxLineBytes(3)
	for _, chunk := range []string{"ab", "c\r", "\n"} {
		_, err = l.Write([]byte(chunk))
		require.NoError(t, err)
	}
	_, err = l.Write([]byte("ab"))
	require.NoError(t, err)
	_, err = l.Write([]byte("cd\n"))
	require.ErrorIs(t, err, ErrLineTooLong)
	require.Equal(t, []string{"abc"}, got)
}

func TestLinesCallbackError(t *testing.T) {
	calls := 0
	l := NewLines(func([]byte) error {
		calls++
		return errCallback
	}, finishEmpty)
	n, err := l.Write([]byte("a\nb\n"))
	require.ErrorIs(t, err, errCallback)
	require.Zero(t, n)
	_, err = l.Write([]byte("c\n"))
	require.ErrorIs(t, err, errCallback)
	_, err = l.ReadFrom(strings.NewReader("d\n"))
	require.ErrorIs(t, err, errCallback)
	require.Equal(t, 1, calls)

	// a failing final line surfaces from Finish
	l = NewLines(func([]byte) error { return errCallback }, finishEmpty)
	_, err = l.Write([]byte("a"))
	require.NoError(t, err)
	_, err = l.Finish()
	require.ErrorIs(t, err, errCallback)
}

func TestLinesFinished(t *testing.T) {
	l := NewLines(func([]byte) error { return nil }, finishEmpty)
	_, err := l.Finish()
	require.NoError(t, err)
	_, err = l.Finish()
	require.ErrorIs(t, err, ErrFinished)
	_, err = l.Write([]byte("a"))
	require.ErrorIs(t, err, ErrFinished)
	_, err = l.ReadFrom(strings.NewReader("a"))
	require.ErrorIs(t, err, ErrFinished)
}

func TestLinesReadFrom(t *testing.T) {
	body := "a\nbb\r\nccc"
	readers := map[string]io.Reader{
		"writer-to": bytes.NewReader([]byte(body)),
		"reader":    iotest.HalfReader(strings.NewReader(body)),
		"data-err":  iotest.DataErrReader(strings.NewReader(body)),
	}
	for name, r := range readers {
		t.Run(name, func(t *testing.T) {
			var got []string
			l := NewLines(collectLines(&got), finishEmpty)
			n, err := l.ReadFrom(r)
			require.NoError(t, err)
			require.Equal(t, int64(len(body)), n)
			_, err = l.Finish()
			require.NoError(t, err)
			require.Equal(t, []string{"a", "bb", "ccc"}, got)
		})
	}
}

type errWriterTo struct{}

func (errWriterTo) Read([]byte) (int, error)         { return 0, io.EOF }
func (errWriterTo) WriteTo(io.Writer) (int64, error) { return 0, io.ErrShortWrite }

func TestLinesReadFromErrors(t *testing.T) {
	l := NewLines(func([]byte) error { return nil }, finishEmpty)
	_, err := l.ReadFrom(io.MultiReader(strings.NewReader("a\n"), iotest.ErrReader(errCallback)))
	require.ErrorIs(t, err, errCallback)
	_, err = l.Finish()
	require.ErrorIs(t, err, errCallback)

	l = NewLines(func([]byte) error { return nil }, finishEmpty)
	_, err = l.ReadFrom(errWriterTo{})
	require.ErrorIs(t, err, io.ErrShortWrite)

	l = NewLines(func([]byte) error { return errCallback }, finishEmpty)
	_, err = l.ReadFrom(iotest.OneByteReader(strings.NewReader("a\nb\n")))
	require.ErrorIs(t, err, errCallback)
}

func TestSplitFields(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"", []string{""}},
		{"a", []string{"a"}},
		{"a\tb\t\tc", []string{"a", "b", "", "c"}},
		{"a\t", []string{"a", ""}},
	}
	var dst [][]byte
	for _, test := range tests {
		dst = SplitFields([]byte(test.line), '\t', dst)
		got := make([]string, len(dst))
		for i, f := range dst {
			got[i] = string(f)
		}
		require.Equal(t, test.want, got)
	}
	line := []byte("a\tb\tc")
	dst = make([][]byte, 0, 4)
	allocs := testing.AllocsPerRun(10, func() { dst = SplitFields(line, '\t', dst) })
	require.Zero(t, allocs)
}
