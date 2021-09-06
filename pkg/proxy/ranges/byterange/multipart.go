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

//go:generate msgp

package byterange

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
)

// MultipartByteRange represents one part of a list of multipart byte ranges
type MultipartByteRange struct {
	Range   Range  `msg:"range"`
	Content []byte `msg:"content"`
}

// MultipartByteRanges is a list of type MultipartByteRange
type MultipartByteRanges map[Range]*MultipartByteRange

// Merge merges the source MultipartByteRanges map into the subject map
func (mbrs MultipartByteRanges) Merge(src MultipartByteRanges) {
	if src == nil || len(src) == 0 || mbrs == nil {
		return
	}
	for _, v := range src.Ranges() {
		mbrs[v] = src[v]
	}
	mbrs.Compress()
}

// PackableMultipartByteRanges returns a version of the subject MultipartByteRanges map
// that is packable by most marshallers, which may require that maps have a key type of string
func (mbrs MultipartByteRanges) PackableMultipartByteRanges() map[string]*MultipartByteRange {
	out := make(map[string]*MultipartByteRange)
	for r, p := range mbrs {
		out[r.String()] = p
	}
	return out
}

// Body returns http headers and body representing the subject MultipartByteRanges map,
// which is suitable for responding to an HTTP request for the full cached range
func (mbrs MultipartByteRanges) Body(fullContentLength int64, contentType string) (http.Header, []byte) {

	ranges := mbrs.Ranges()
	if ranges == nil || len(ranges) == 0 {
		return nil, []byte{}
	}

	// if just one range part, return a normal range response (not multipart)
	if len(ranges) == 1 {
		r := ranges[0]
		return http.Header{
			headers.NameContentType:  []string{contentType},
			headers.NameContentRange: []string{mbrs[r].Range.ContentRangeHeader(fullContentLength)},
		}, mbrs[r].Content
	}

	// otherwise, we return a multipart response

	sort.Sort(ranges)

	boundary := md5.Checksum(ranges.String())
	var bw = bytes.NewBuffer(make([]byte, 0))
	mw := multipart.NewWriter(bw)
	mw.SetBoundary(boundary)

	for _, r := range ranges {
		pw, err := mw.CreatePart(
			textproto.MIMEHeader{
				headers.NameContentType:  []string{contentType},
				headers.NameContentRange: []string{mbrs[r].Range.ContentRangeHeader(fullContentLength)},
			},
		)
		if err != nil {
			continue
		}
		pw.Write(mbrs[r].Content)
	}
	mw.Close()

	return http.Header{
		headers.NameContentType: []string{headers.ValueMultipartByteRanges + boundary},
	}, bw.Bytes()

}

// Ranges returns a Ranges object from the MultipartByteRanges Object
func (mbrs MultipartByteRanges) Ranges() Ranges {
	if len(mbrs) == 0 {
		return Ranges{}
	}
	ranges := make(Ranges, 0, len(mbrs))
	for _, v := range mbrs {
		ranges = append(ranges, v.Range)
	}
	sort.Sort(ranges)
	return ranges
}

// Compress will take a Multipart Byte Range Map and compress it such that adajecent ranges are merged
func (mbrs MultipartByteRanges) Compress() {

	if len(mbrs.Ranges()) < 2 {
		return
	}

	cnt := 0
	for len(mbrs) != cnt {
		cnt = len(mbrs)
		var prev *MultipartByteRange
		for _, r := range mbrs.Ranges() {
			curr := mbrs[r]
			if prev != nil && r.Start == prev.Range.End+1 {

				newPart := &MultipartByteRange{Range: Range{Start: prev.Range.Start, End: curr.Range.End}}
				l := newPart.Range.End - newPart.Range.Start + 1
				body := make([]byte, l)

				copy(body[:len(prev.Content)], prev.Content[:])
				copy(body[len(prev.Content):], curr.Content[:])
				newPart.Content = body
				delete(mbrs, r)
				delete(mbrs, prev.Range)
				mbrs[newPart.Range] = newPart
				curr = newPart
			}
			prev = curr
		}
	}

}

// ParseMultipartRangeResponseBody returns a MultipartByteRanges from the provided body
func ParseMultipartRangeResponseBody(body io.Reader,
	contentTypeHeader string) (MultipartByteRanges, string, Ranges, int64, error) {
	parts := make(MultipartByteRanges)
	ranges := make(Ranges, 0)
	fullContentLength := int64(-1)
	ct := ""
	if strings.HasPrefix(contentTypeHeader, headers.ValueMultipartByteRanges) {
		separator := contentTypeHeader[len(headers.ValueMultipartByteRanges):]
		if separator != "" {
			mr := multipart.NewReader(body, separator)
			for {
				p, err := mr.NextPart()
				if err != nil {
					// EOF triggers the end of the loop, but
					// it can sometimes come in as "multipart: NextPart: EOF"
					// so it is more reliable to check for EOF as a suffix
					if strings.HasSuffix(err.Error(), "EOF") {
						break
					}
					return nil, "", nil, -1, err
				}

				if _, ok := p.Header[headers.NameContentRange]; ok {
					r, rcl, err := ParseContentRangeHeader(p.Header.Get(headers.NameContentRange))
					if ct == "" {
						ct = p.Header.Get(headers.NameContentType)
					}

					fullContentLength = rcl
					if err != nil {
						return nil, "", nil, -1, err
					}
					ranges = append(ranges, r)
					bdy, err := io.ReadAll(p)
					if err != nil {
						return nil, "", nil, -1, err
					}
					mpbr := &MultipartByteRange{
						Range:   r,
						Content: bdy,
					}
					parts[r] = mpbr
				}
			}
		}
	}
	return parts, ct, ranges, fullContentLength, nil
}

// ExtractResponseRange returns http headers and body representing the subject MultipartByteRanges map,
// cropped to the provided ranges
func (mbrs MultipartByteRanges) ExtractResponseRange(ranges Ranges, fullContentLength int64,
	contentType string, body []byte) (http.Header, []byte) {

	if ranges == nil || len(ranges) == 0 {
		return nil, body
	}

	useBody := len(mbrs) == 0
	if useBody {
		if body == nil {
			return nil, nil
		}
		fullContentLength = int64(len(body))
	}

	m := make(MultipartByteRanges)

	for _, r := range ranges {
		rcl := (r.End - r.Start) + 1
		mbr := &MultipartByteRange{Range: r, Content: make([]byte, rcl)}

		if useBody {
			copy(mbr.Content[:], body[r.Start:r.End+1])
		} else {
			brs := mbrs.Ranges()
			if brs != nil {
				for _, r2 := range brs {

					p := mbrs[r2]

					if r.Start >= p.Range.Start && r.End <= p.Range.End {

						// unsure if we need this depending upon how ranges are filled and compressed
						// so leaving it present but commented for now.
						startOffset := r.Start - p.Range.Start
						endOffset := (r.End - p.Range.Start) + 1
						// mbr.Content = p.Content[startOffset : int64(len(p.Content))-endOffset]

						// and the shortcut alternative method that seems to work for current use cases
						mbr.Content = p.Content[startOffset:endOffset]
						break
					}
				}
			}
		}

		m[r] = mbr

	}
	return m.Body(fullContentLength, contentType)
}
