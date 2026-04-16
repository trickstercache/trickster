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

package byterange

import (
	"bytes"
	"testing"
)

func TestRangeRoundTrip(t *testing.T) {
	v := Range{Start: 100, End: 200}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Range
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Start != 100 {
		t.Fatal("Start mismatch")
	}
	if v2.End != 200 {
		t.Fatal("End mismatch")
	}
}

func TestRangesRoundTrip(t *testing.T) {
	v := Ranges{
		{Start: 10, End: 20},
		{Start: 30, End: 40},
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Ranges
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2) != 2 {
		t.Fatal("expected 2 ranges")
	}
	if v2[0].Start != 10 {
		t.Fatal("first range Start mismatch")
	}
	if v2[1].End != 40 {
		t.Fatal("second range End mismatch")
	}
}

func TestMultipartByteRangeRoundTrip(t *testing.T) {
	v := MultipartByteRange{
		Range:   Range{Start: 50, End: 99},
		Content: []byte("hello world"),
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 MultipartByteRange
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Range.Start != 50 {
		t.Fatal("Range.Start mismatch")
	}
	if v2.Range.End != 99 {
		t.Fatal("Range.End mismatch")
	}
	if !bytes.Equal(v2.Content, []byte("hello world")) {
		t.Fatal("Content mismatch")
	}
}
