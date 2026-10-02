/*
 * Copyright 2026 The Trickster Authors
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

// Package normalize cleans request paths before routing, so the route a request
// matches and the path its origin receives always agree.
package normalize

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize/options"
)

type dotMode uint8

const (
	dotsNormalize dotMode = iota
	dotsReject
	dotsOff
)

type slashMode uint8

const (
	slashesKeep slashMode = iota
	slashesReject
	slashesUnescape
)

const (
	separator        = '/'
	separatorStr     = "/"
	doubledSeparator = "//"
	dot              = '.'
	escapeMarker     = '%'
	// escapeLen is the length of a percent-encoded octet, such as %2F
	escapeLen = 3
)

// Normalizer applies one listener's path normalization options to request URLs.
type Normalizer struct {
	dots    dotMode
	merge   bool
	slashes slashMode
}

// New returns a Normalizer for the options; nil options select the defaults.
func New(o *options.Options) *Normalizer {
	r := o.Resolved()
	n := &Normalizer{merge: r.MergeSlashes}
	switch r.DotSegments {
	case options.DotSegmentsReject:
		n.dots = dotsReject
	case options.DotSegmentsOff:
		n.dots = dotsOff
	}
	switch r.EscapedSlashes {
	case options.EscapedSlashesReject:
		n.slashes = slashesReject
	case options.EscapedSlashesUnescape:
		n.slashes = slashesUnescape
	}
	return n
}

// Enabled reports whether the Normalizer can change or refuse any path.
func (n *Normalizer) Enabled() bool {
	return n.dots != dotsOff || n.merge || n.slashes != slashesKeep
}

// Middleware normalizes each request's path before next routes it, and
// answers 400 Bad Request for a path the options refuse.
func Middleware(o *options.Options, next http.Handler) http.Handler {
	n := New(o)
	if next == nil || !n.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL != nil && !n.Normalize(r.URL) {
			failures.HandleBadRequestResponse(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Normalize rewrites u's path in place and reports false when the request must
// be refused. A path needing no change is left as is, without allocating.
func (n *Normalizer) Normalize(u *url.URL) bool {
	p := u.Path
	if p == "" || p[0] != separator {
		return true
	}
	if u.RawPath != "" && n.slashes != slashesKeep && hasEscapedSlash(u.RawPath) {
		if n.slashes == slashesReject {
			return false
		}
		// Path already holds each %2F decoded, so dropping RawPath makes them separators
		u.RawPath = ""
	}
	// the decoded path shows every dot-segment, including one an encoded slash hides
	dots := n.dots != dotsOff && hasDotSegment(p)
	if dots && n.dots == dotsReject {
		return false
	}
	resolve := dots && n.dots == dotsNormalize
	merge := n.merge && strings.Contains(p, doubledSeparator)
	if !resolve && !merge {
		return true
	}
	if u.RawPath == "" {
		u.Path, _ = clean(p, resolve, merge, false)
		return true
	}
	// with escaped slashes kept, segments are split on literal slashes only
	out, ok := clean(u.EscapedPath(), resolve, merge, true)
	if !ok {
		return false
	}
	decoded, err := url.PathUnescape(out)
	if err != nil {
		return false
	}
	u.Path, u.RawPath = decoded, out
	return true
}

func hasDotSegment(p string) bool {
	// reports whether p holds a "." or ".." segment, searching for the dot rather
	// than the slash, since dots are rare in paths and slashes are not
	for i := strings.IndexByte(p, dot); i >= 0; {
		if i > 0 && p[i-1] == separator {
			rest := p[i+1:]
			if rest == "" || rest[0] == separator ||
				(rest[0] == dot && (len(rest) == 1 || rest[1] == separator)) {
				return true
			}
		}
		j := strings.IndexByte(p[i+1:], dot)
		if j < 0 {
			return false
		}
		i += j + 1
	}
	return false
}

func clean(p string, resolve, merge, escaped bool) (string, bool) {
	// removes dot-segments (RFC 3986 section 5.2.4) and merges slashes; on an escaped path, %2E is a dot,
	// and a segment whose %2F hides a dot-segment is refused, since origins disagree on whether %2F separates
	b := make([]byte, 0, len(p))
	rest := p[1:]
	for {
		seg, next, more := strings.Cut(rest, separatorStr)
		var units int
		if resolve {
			if escaped && hidesDotSegment(seg) {
				return "", false
			}
			units = dotUnits(seg, escaped)
		}
		switch {
		case units == 1:
			if !more {
				b = append(b, separator)
			}
		case units == 2:
			if i := bytes.LastIndexByte(b, separator); i >= 0 {
				b = b[:i]
			}
			if !more {
				b = append(b, separator)
			}
		case merge && more && seg == "":
		default:
			b = append(b, separator)
			b = append(b, seg...)
		}
		if !more {
			break
		}
		rest = next
	}
	if len(b) == 0 {
		return "/", true
	}
	return string(b), true
}

func dotUnits(seg string, escaped bool) int {
	// 1 for a "." segment, 2 for "..", and 0 otherwise; with escaped set, %2E
	// counts as a dot (RFC 3986 section 6.2.2.2)
	var n int
	for seg != "" {
		switch {
		case seg[0] == dot:
			seg = seg[1:]
		case escaped && isEscapedOctet(seg, 'E'):
			seg = seg[escapeLen:]
		default:
			return 0
		}
		if n++; n > 2 {
			return 0
		}
	}
	return n
}

func hidesDotSegment(seg string) bool {
	// whether an escaped segment would hold a dot-segment once its encoded
	// slashes were decoded, as in "..%2Fadmin"
	i := indexEscapedSlash(seg)
	if i < 0 {
		return false
	}
	for {
		if dotUnits(seg[:i], true) > 0 {
			return true
		}
		seg = seg[i+escapeLen:]
		if i = indexEscapedSlash(seg); i < 0 {
			return dotUnits(seg, true) > 0
		}
	}
}

func hasEscapedSlash(s string) bool {
	return indexEscapedSlash(s) >= 0
}

func indexEscapedSlash(s string) int {
	var off int
	for {
		i := strings.IndexByte(s[off:], escapeMarker)
		if i < 0 {
			return -1
		}
		if isEscapedOctet(s[off+i:], 'F') {
			return off + i
		}
		off += i + 1
	}
}

func isEscapedOctet(s string, upper byte) bool {
	// whether s begins with %2 followed by upper or its lower case
	return len(s) >= escapeLen && s[0] == escapeMarker && s[1] == '2' &&
		(s[2] == upper || s[2] == upper+('a'-'A'))
}
