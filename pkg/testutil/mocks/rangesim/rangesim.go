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

// Package rangesim is a mock HTTP origin that serves a fixed text body with
// full support for Range requests, for unit, integration and dev-env testing.
package rangesim

import (
	"cmp"
	"io"
	"mime/multipart"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Body is the content that rangesim serves; responses repeat it to reach the
// requested size.
const Body = `Lorem ipsum dolor sit amet, mel alia definiebas ei, labore eligendi ` +
	`signiferumque id sed. Dico tantas fabulas et vel, maiorum splendide has an. Te mea ` +
	`suas commune concludaturque. Qui feugait tacimates te.

` + `Ea sea error altera efficiantur, ex possit appetere eum. Sed cu sanctus blandit definiebas, ` +
	`movet accumsan no mei. Vim diam molestie singulis cu, et sanctus appetere ius, his ut ` +
	`consulatu vituperata. Graece graeco sit ut, an quem summo splendide duo. Iisque ` +
	`sapientem interpretaris pro ad, alii mazim pro te. Malis laoreet facilis sea te. An ` +
	`ferri albucius vel, altera volumus legendos has in.

` + `His ne dolore rationibus. Ut qui ferri malorum. Mel commune atomorum cu. Ut mollis ` +
	`reprimique nam, eos quot mutat molestie id. Mea error legere contentiones et, ponderum ` +
	`accusamus est eu. Detraxit repudiandae signiferumque ne eos.

` + `Ius ne periculis consequat, ea usu brute mediocritatem, an qui reque falli deseruisse. ` +
	`Vix ne aeque movet. Novum homero referrentur in est. No mei adhuc malorum.

` + `Pri vitae sapientem ad, qui libris prompta ei. Ne quem fabulas dissentiet cum, error ` +
	`legimus vis cu. Te eum lorem liber aliquando, eirmod diceret vis ad. Eos et facer tation. ` +
	`Etiam phaedrum ea est, an nec summo mediocritatem.

`

const (
	contentLength = int64(len(Body))
	contentType   = "text/plain; charset=utf-8"
	separator     = "TestRangeServerBoundary"
	maxAge        = "max-age=60"

	hnAcceptRanges    = "Accept-Ranges"
	hnCacheControl    = "Cache-Control"
	hnContentRange    = "Content-Range"
	hnContentType     = "Content-Type"
	hnContentLength   = "Content-Length"
	hnIfModifiedSince = "If-Modified-Since"
	hnLastModified    = "Last-Modified"
	hnRange           = "Range"

	// MaxSize is the largest response length the size parameter may request.
	MaxSize   = 1 << 30
	maxRanges = 64 // more ranges get the full body, as http.ServeContent does for abusive sets

	hvMultipartByteRange    = "multipart/byteranges; boundary="
	byteRequestRangePrefix  = "bytes="
	byteResponseRangePrefix = "bytes "
)

// LastModified is the Last-Modified time of Body: 1 January 2020 00:00:00 UTC.
var LastModified = time.Unix(1577836800, 0)

type byteRange struct {
	start int64
	end   int64
}

type byteRanges []byteRange

func (brs byteRanges) validate(cl int64) bool {
	for _, r := range brs {
		if r.start < 0 || r.end >= cl || r.end < r.start {
			return false
		}
	}
	return true
}

func (brs byteRanges) totalLength() int64 {
	// validated ranges are each at most MaxSize long, and there are at most
	// maxRanges of them, so the sum cannot overflow
	var n int64
	for _, r := range brs {
		n += r.end - r.start + 1
	}
	return n
}

func (br byteRange) contentRangeHeader(cl int64) string {
	return byteResponseRangePrefix + strconv.FormatInt(br.start, 10) + "-" +
		strconv.FormatInt(br.end, 10) + "/" + strconv.FormatInt(cl, 10)
}

func (brs byteRanges) writeMultipartResponse(cl int64, w io.Writer) error {
	mw := multipart.NewWriter(w)
	mw.SetBoundary(separator)
	for _, r := range brs {
		pw, err := mw.CreatePart(
			textproto.MIMEHeader{
				hnContentType:  []string{contentType},
				hnContentRange: []string{r.contentRangeHeader(cl)},
			},
		)
		if err != nil {
			return err
		}
		if err := writeRange(pw, r); err != nil {
			return err
		}
	}
	return mw.Close()
}

func parseRangeHeader(input string) byteRanges {
	// nil for anything not fully parseable; missing bounds are -1
	if input == "" || !strings.HasPrefix(input, byteRequestRangePrefix) ||
		input == byteRequestRangePrefix {
		return nil
	}
	input = strings.ReplaceAll(input, " ", "")[len(byteRequestRangePrefix):]
	if strings.Count(input, ",") >= maxRanges {
		return nil
	}
	parts := strings.Split(input, ",")
	ranges := make(byteRanges, len(parts))
	for i, p := range parts {
		j := strings.Index(p, "-")
		if j < 0 {
			return nil
		}
		start, end := int64(-1), int64(-1)
		var err error
		if j > 0 {
			start, err = strconv.ParseInt(p[:j], 10, 64)
			if err != nil {
				return nil
			}
		}
		if j < len(p)-1 {
			end, err = strconv.ParseInt(p[j+1:], 10, 64)
			if err != nil {
				return nil
			}
		}
		ranges[i].start = start
		ranges[i].end = end
	}
	slices.SortFunc(ranges, func(a, b byteRange) int { return cmp.Compare(a.end, b.end) })
	return ranges
}

func writeRange(w io.Writer, br byteRange) error {
	// validate guarantees 0 <= start <= end < MaxInt64, so the length cannot overflow;
	// ranges past the end of Body wrap around it, like the full-body response
	remaining := br.end - br.start + 1
	offset := br.start % contentLength
	for remaining > 0 {
		n := min(contentLength-offset, remaining)
		if _, err := io.WriteString(w, Body[offset:offset+n]); err != nil {
			return err
		}
		remaining -= n
		offset = 0
	}
	return nil
}
