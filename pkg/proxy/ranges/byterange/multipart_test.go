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
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const testSeparator = "TEST-SEPARATOR"
const testRange1 = "0-49"
const testRange2 = "100-149"
const testContentLength = "150"
const testContentType1 = headers.ValueTextPlain
const testPart1Body = "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwx"
const testPart2Body = `{ "body": "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJ" }`

var content = []byte(fmt.Sprintf(`--%s
Content-Type: %s
Content-Range: bytes %s/%s

%s
--%s
Content-Type: %s
Content-Range: bytes %s/%s

%s
--%s--`, testSeparator, testContentType1, testRange1, testContentLength,
	testPart1Body, testSeparator, testContentType1, testRange2, testContentLength,
	testPart2Body, testSeparator))

func TestParseMultipartRangeResponseBody(t *testing.T) {

	reader := io.NopCloser(bytes.NewBuffer(content))

	parts, ct, ranges, cl, err := ParseMultipartRangeResponseBody(reader,
		headers.ValueMultipartByteRanges+testSeparator)
	if err != nil {
		t.Error(err)
	}

	if cl != 150 {
		t.Errorf("expected %d, got %d", 150, cl)
	}

	if parts == nil {
		t.Errorf("expected 2 parts, got %v", parts)
	} else if len(parts) != 2 {
		t.Errorf("expected 2 parts, got %d", len(parts))
	}

	if parts == nil {
		t.Errorf("expected 2 ranges, got %v", ranges)
	} else if len(parts) != 2 {
		t.Errorf("expected 2 ranges got %d", len(ranges))
	}

	if ct != testContentType1 {
		t.Errorf("expected %s, got %s", testContentType1, ct)
	}

	if parts[ranges[0]].Range.String() != testRange1 {
		t.Errorf("expected %s, got %s", testRange1, parts[ranges[0]].Range.String())
	}

	if string(parts[ranges[0]].Content) != testPart1Body {
		t.Errorf("expected %s, got %s", testPart1Body, parts[ranges[0]].Range.String())
	}

	if parts[ranges[1]].Range.String() != testRange2 {
		t.Errorf("expected %s, got %s", testRange2, parts[ranges[1]].Range.String())
	}

	if ct != testContentType1 {
		t.Errorf("expected %s, got %s", testContentType1, ct)
	}

	if string(parts[ranges[1]].Content) != testPart2Body {
		t.Errorf("expected %s, got %s", testPart1Body, string(parts[ranges[1]].Content))
	}
}

func testArtifacts() (MultipartByteRanges, MultipartByteRanges) {

	r1 := &MultipartByteRange{
		Range:   Range{Start: 0, End: 6},
		Content: []byte("Lorem i"),
	}

	r2 := &MultipartByteRange{
		Range:   Range{Start: 10, End: 20},
		Content: []byte("m dolor sit"),
	}

	m1 := MultipartByteRanges{
		r1.Range: r1,
		r2.Range: r2,
	}

	r3 := &MultipartByteRange{
		Range:   Range{Start: 60, End: 65},
		Content: []byte("ligend"),
	}

	r4 := &MultipartByteRange{
		Range:   Range{Start: 69, End: 75},
		Content: []byte("ignifer"),
	}

	m2 := MultipartByteRanges{
		r3.Range: r3,
		r4.Range: r4,
	}

	return m1, m2

}

func TestMerge(t *testing.T) {

	m1, m2 := testArtifacts()
	m1.Merge(m2)

	if len(m1) != 4 {
		t.Errorf("expected %d got %d", 4, len(m1))
	}

	// coverage for short bail out condition
	m1.Merge(nil)
	if len(m1) != 4 {
		t.Errorf("expected %d got %d", 4, len(m1))
	}

}

func TestPackableMultipartByteRanges(t *testing.T) {
	m1, _ := testArtifacts()
	m2 := m1.PackableMultipartByteRanges()
	if len(m2) != 2 {
		t.Errorf("expected %d got %d", 2, len(m2))
	}
}

func TestBody(t *testing.T) {

	m1, _ := testArtifacts()
	// test multiple range
	h, b := m1.Body(1222, "text/plain")
	if !strings.Contains(string(b), "m dolor sit") {
		t.Errorf("expected %d, got %d", 240, len(b))
	}
	if !strings.HasPrefix(h.Get(headers.NameContentType), "multipart/byteranges") {
		t.Errorf("expected %s, got %s", "multipart/byteranges", h.Get(headers.NameContentType))
	}

	delete(m1, m1.Ranges()[1])

	h, b = m1.Body(1222, "text/plain")
	if strings.Contains(string(b), "m dolor sit") {
		t.Errorf("expected %d, got %d", 240, len(b))
	}

	if !strings.Contains(string(b), "Lorem i") {
		t.Errorf("expected %d, got %d", 240, len(b))
	}

	if !strings.HasPrefix(h.Get(headers.NameContentType), "text/plain") {
		t.Errorf("expected %s, got %s", "text/plain", h.Get(headers.NameContentType))
	}

	m2 := make(MultipartByteRanges)
	h, b = m2.Body(1, "test")

	if h != nil {
		t.Errorf("expected nil header, got %v", h)
	}

	if len(b) != 0 {
		t.Errorf("expected %d got %d", 0, len(b))
	}

}

func TestCompress(t *testing.T) {

	m1, m2 := testArtifacts()

	r1 := &MultipartByteRange{
		Range:   Range{Start: 7, End: 9},
		Content: []byte("psu"),
	}
	m1[r1.Range] = r1
	m1.Compress()

	if len(m1) != 1 {
		t.Errorf("expected %d got %d", 1, len(m1))
	}

	// test short circuit case
	delete(m2, m2.Ranges()[1])
	m2.Compress()
	if len(m2) != 1 {
		t.Errorf("expected %d got %d", 1, len(m2))
	}

}

func TestExtractResponseRange(t *testing.T) {

	m1, _ := testArtifacts()

	r := Ranges{Range{Start: 12, End: 15}}

	h, b := m1.ExtractResponseRange(r, 60, "test", nil)

	if v := h.Get(headers.NameContentType); v != "test" {
		t.Errorf("expected %s got %s", "test", v)
	}

	if v := h.Get(headers.NameContentRange); v != "bytes 12-15/60" {
		t.Errorf("expected %s got %s", "bytes 12-15/60", v)
	}

	const expected = "dolo"
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}

	// test empty range
	h, b = m1.ExtractResponseRange(nil, 60, "test", nil)
	if h != nil {
		t.Errorf("expected nil headers, got %v", h)
	}
	if b != nil {
		t.Errorf("expected nil body, got %s", string(b))
	}

	// test empty map
	m3 := make(MultipartByteRanges)
	h, b = m3.ExtractResponseRange(r, 60, "test", nil)
	if h != nil {
		t.Errorf("expected nil headers, got %v", h)
	}
	if b != nil {
		t.Errorf("expected nil body, got %s", string(b))
	}

	// test content length
	h, b = m1.ExtractResponseRange(r, -1, "test", nil)
	if v := h.Get(headers.NameContentRange); v != "bytes 12-15/*" {
		t.Errorf("expected %s got %s", "bytes 12-15/*", v)
	}
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}

	h, b = m1.ExtractResponseRange(r, -1, "test", []byte("test body is large"))
	if v := h.Get(headers.NameContentRange); v != "bytes 12-15/*" {
		t.Errorf("expected %s got %s", "bytes 12-15/*", v)
	}
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}

	// test useBody
	r[0].Start = 13
	r[0].End = 17
	h, b = m3.ExtractResponseRange(r, -1, "test", []byte("test body is large"))
	if v := h.Get(headers.NameContentRange); v != "bytes 13-17/18" {
		t.Errorf("expected %s got %s", "bytes 13-17/18", v)
	}
	if string(b) != "large" {
		t.Errorf("expected %s got %s", "large", string(b))
	}

}
