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

package rewriter

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

type rewriteInstruction interface {
	String() string
	Parse([]string) error
	Execute(r *http.Request)
	HasTokens() bool
}

// RewriteInstructions is a list of type []rewriteInstruction
type RewriteInstructions []rewriteInstruction

var rewriters = map[string]func() rewriteInstruction{
	"scheme-set":       func() rewriteInstruction { return &rwiBasicSetter{} },
	"header-set":       func() rewriteInstruction { return &rwiKeyBasedSetter{} },
	"header-replace":   func() rewriteInstruction { return &rwiKeyBasedReplacer{} },
	"header-delete":    func() rewriteInstruction { return &rwiKeyBasedDeleter{} },
	"header-append":    func() rewriteInstruction { return &rwiKeyBasedAppender{} },
	"path-set":         func() rewriteInstruction { return &rwiPathSetter{} },
	"path-replace":     func() rewriteInstruction { return &rwiPathReplacer{} },
	"param-set":        func() rewriteInstruction { return &rwiKeyBasedSetter{} },
	"param-replace":    func() rewriteInstruction { return &rwiKeyBasedReplacer{} },
	"param-delete":     func() rewriteInstruction { return &rwiKeyBasedDeleter{} },
	"param-append":     func() rewriteInstruction { return &rwiKeyBasedAppender{} },
	"params-set":       func() rewriteInstruction { return &rwiBasicSetter{} },
	"params-replace":   func() rewriteInstruction { return &rwiBasicReplacer{} },
	"method-set":       func() rewriteInstruction { return &rwiBasicSetter{} },
	"host-set":         func() rewriteInstruction { return &rwiBasicSetter{} },
	"host-replace":     func() rewriteInstruction { return &rwiBasicReplacer{} },
	"hostname-set":     func() rewriteInstruction { return &rwiBasicSetter{} },
	"hostname-replace": func() rewriteInstruction { return &rwiBasicReplacer{} },
	"port-set":         func() rewriteInstruction { return &rwiBasicSetter{} },
	"port-replace":     func() rewriteInstruction { return &rwiBasicReplacer{} },
	"port-delete":      func() rewriteInstruction { return &rwiPortDeleter{} },
	"chain-exec":       func() rewriteInstruction { return &rwiChainExecutor{} },
}

type dictable interface {
	Get(string) string
	Set(string, string)
	Del(string)
}

type dictFunc func(*http.Request) dictable

var dicts = map[string]dictFunc{
	"header": func(r *http.Request) dictable {
		if r == nil {
			return nil
		}
		return r.Header
	},
	"param": func(r *http.Request) dictable {
		if r == nil || r.URL == nil {
			return nil
		}
		return r.URL.Query()
	},
}

type scalarGetFunc func(*http.Request) string
type scalarSetFunc func(*http.Request, string)

var scalarGets = map[string]scalarGetFunc{
	"params": func(r *http.Request) string {
		if r == nil || r.URL == nil {
			return ""
		}
		return r.URL.RawQuery
	},
	"method": func(r *http.Request) string {
		if r == nil {
			return ""
		}
		return r.Method
	},
	"host": func(r *http.Request) string {
		if r == nil || r.URL == nil {
			return ""
		}
		return r.URL.Host
	},
	"hostname": func(r *http.Request) string {
		if r == nil || r.URL == nil {
			return ""
		}
		return r.URL.Hostname()
	},
	"port": func(r *http.Request) string {
		if r == nil || r.URL == nil {
			return ""
		}
		return r.URL.Port()
	},
}

var scalarSets = map[string]scalarSetFunc{
	"scheme": func(r *http.Request, v string) {
		if r != nil && r.URL != nil {
			r.URL.Scheme = v
		}
	},
	"params": func(r *http.Request, v string) {
		if r != nil && r.URL != nil {
			r.URL.RawQuery = v
		}
	},
	"method": func(r *http.Request, v string) {
		if r != nil {
			r.Method = v
		}
	},
	"host": func(r *http.Request, v string) {
		if r != nil && r.URL != nil {
			r.URL.Host = v
		}
	},
	"hostname": func(r *http.Request, v string) {
		if r != nil && r.URL != nil {
			h := r.URL.Host
			var port string
			if i := strings.Index(h, ":"); i > 0 {
				port = h[i:]
			}
			r.URL.Host = v + port
		}
	},
	"port": func(r *http.Request, v string) {
		if r == nil || r.URL == nil {
			return
		}
		h := r.URL.Host
		var port string
		if i := strings.Index(h, ":"); i > 0 {
			h = h[:i]
		}
		if v != "" {
			port = ":" + v
		}
		r.URL.Host = h + port
	},
}

func (ris RewriteInstructions) String() string {
	l := make([]string, len(ris))
	for i, instr := range ris {
		l[i] = instr.String()
	}
	return "[" + strings.Join(l, ",") + "]"
}

// Execute executes the Rewriter Instructions on the provided HTTP Request
func (ris RewriteInstructions) Execute(r *http.Request) {
	for _, instr := range ris {
		instr.Execute(r)
	}
}

func checkTokens(input string) bool {
	i := strings.Index(input, "${")
	if i > -1 && strings.Index(input, "}") > i {
		return true
	}
	return false
}

type rwiKeyBasedSetter struct {
	key, value string
	hasTokens  bool
	dict       dictFunc
}

func (ri *rwiKeyBasedSetter) String() string {
	return fmt.Sprintf(`{"type":"keyBasedSetter","key":"%s","value": "%s","tokens": "%t"}`,
		ri.key, ri.value, ri.hasTokens)
}

func (ri *rwiKeyBasedSetter) Parse(parts []string) error {
	if len(parts) != 4 {
		return errBadParams
	}
	var ok bool
	if ri.dict, ok = dicts[parts[0]]; !ok {
		return errBadParams
	}
	ri.key = parts[2]
	ri.value = parts[3]
	ri.hasTokens = checkTokens(ri.value)
	return nil
}

func (ri *rwiKeyBasedSetter) Execute(r *http.Request) {
	dict := ri.dict(r)
	dict.Set(ri.key, ri.value)
	if qp, ok := dict.(url.Values); ok {
		r.URL.RawQuery = qp.Encode()
	}
}

func (ri *rwiKeyBasedSetter) HasTokens() bool {
	return ri.hasTokens
}

type rwiKeyBasedAppender struct {
	key, value string
	hasTokens  bool
	dict       dictFunc
}

func (ri *rwiKeyBasedAppender) String() string {
	return fmt.Sprintf(`{"type":"rwiKeyBasedAppender","key":"%s","value": "%s","tokens": "%t"}`,
		ri.key, ri.value, ri.hasTokens)
}

func (ri *rwiKeyBasedAppender) Parse(parts []string) error {
	if len(parts) != 4 {
		return errBadParams
	}
	var ok bool
	if ri.dict, ok = dicts[parts[0]]; !ok {
		return errBadParams
	}
	ri.key = parts[2]
	ri.value = parts[3]
	ri.hasTokens = checkTokens(ri.value)
	return nil
}

type mappable map[string][]string

func (ri *rwiKeyBasedAppender) Execute(r *http.Request) {

	dict := ri.dict(r)
	var m mappable
	var ok bool
	var h http.Header
	var q url.Values
	var vals []string

	switch v := dict.(type) {
	case http.Header:
		h = v
		m = mappable(h)
	case url.Values:
		q = v
		m = mappable(q)
	}

	vals, ok = m[ri.key]
	// key does not exist, so set value instead of appending
	if !ok {
		dict.Set(ri.key, ri.value)
		if q != nil {
			r.URL.RawQuery = q.Encode()
		}
		return
	}

	// appending to url param value
	if q != nil {
		for _, v := range vals {
			if v == ri.value {
				// the desired value is already in the query, do nothing
				return
			}
		}
		m[ri.key] = append(vals, ri.value)
		r.URL.RawQuery = q.Encode()
		return
	}

	// appending to header value

	var subkey string
	j := strings.Index(ri.value, "=")
	if j > 0 {
		subkey = ri.value[:j]
	} else {
		subkey = ri.value
	}

	// this might look redundant, but it normalizes something like:
	//  {"header": []string{"val1=abc, val2", "val3=def"}}
	// which should not happen but is technically possible
	parts := strings.Split(strings.Join(vals, ", "), ", ")

	var found bool
	for i, part := range parts {
		if part == ri.value {
			// value exists in header already, nothing to do
			return
		}
		if strings.HasPrefix(part, subkey+"=") {
			// a right-subkey=wrong-value exists, set it to the right value
			parts[i] = ri.value
			found = true
		}
	}

	if !found {
		parts = append(parts, ri.value)
	}

	h.Set(ri.key, strings.Join(parts, ", "))

}

func (ri *rwiKeyBasedAppender) HasTokens() bool {
	return ri.hasTokens
}

type rwiKeyBasedReplacer struct {
	key, search, replacement string
	depth                    int
	hasTokens                bool
	dict                     dictFunc
}

func (ri *rwiKeyBasedReplacer) String() string {
	return fmt.Sprintf(`{"type":"keyBasedReplacer","key":"%s","search":"%s","replacement":"%s","tokens":"%t"}`,
		ri.key, ri.search, ri.replacement, ri.hasTokens)
}

func (ri *rwiKeyBasedReplacer) Parse(parts []string) error {
	if len(parts) != 5 {
		return errBadParams
	}
	var ok bool
	if ri.dict, ok = dicts[parts[0]]; !ok {
		return errBadParams
	}
	ri.key = parts[2]
	ri.search = parts[3]
	ri.replacement = parts[4]
	ri.hasTokens = checkTokens(ri.key) || checkTokens(ri.search) || checkTokens(ri.replacement)
	return nil
}

func (ri *rwiKeyBasedReplacer) Execute(r *http.Request) {

	if ri.depth == 0 {
		ri.depth = -1
	}

	dict := ri.dict(r)
	var m mappable
	var ok bool
	var h http.Header
	var q url.Values
	var vals []string

	switch v := dict.(type) {
	case http.Header:
		h = v
		m = mappable(h)
	case url.Values:
		q = v
		m = mappable(q)
	}

	vals, ok = m[ri.key]
	if !ok {
		return
	}

	for i := range vals {
		vals[i] = strings.Replace(vals[i], ri.search, ri.replacement, ri.depth)
	}
	m[ri.key] = vals

	if q != nil {
		r.URL.RawQuery = q.Encode()
	}
}

func (ri *rwiKeyBasedReplacer) HasTokens() bool {
	return ri.hasTokens
}

type rwiKeyBasedDeleter struct {
	key, value string
	hasTokens  bool
	dict       dictFunc
}

func (ri *rwiKeyBasedDeleter) String() string {
	return fmt.Sprintf(`{"type":"keyBasedDeleter","key":"%s","value":"%s","tokens":"%t"}`,
		ri.key, ri.value, ri.hasTokens)
}

func (ri *rwiKeyBasedDeleter) Parse(parts []string) error {
	pl := len(parts)
	if pl != 3 && pl != 4 {
		return errBadParams
	}
	var ok bool
	if ri.dict, ok = dicts[parts[0]]; !ok {
		return errBadParams
	}

	ri.key = parts[2]
	if pl == 4 {
		ri.value = parts[3]
	}
	ri.hasTokens = checkTokens(ri.key) || checkTokens(ri.value)
	return nil
}

func (ri *rwiKeyBasedDeleter) Execute(r *http.Request) {

	dict := ri.dict(r)

	if ri.value == "" {
		dict.Del(ri.key)
		if qp, ok := dict.(url.Values); ok {
			r.URL.RawQuery = qp.Encode()
		}
		return
	}

	found := -1
	// url params
	if qp, ok := dict.(url.Values); ok {
		if vals, ok1 := qp[ri.key]; ok1 {
			for i, v := range vals {
				if v == ri.value {
					found = i
					break
				}
			}
			if found > -1 {
				qp[ri.key] = append(vals[:found], vals[found+1:]...)
				r.URL.RawQuery = qp.Encode()
			}
		}
		return
	}

	// headers
	val := dict.Get(ri.key)
	parts := strings.Split(val, ", ")
	for i, part := range parts {
		if strings.HasPrefix(part, ri.value+"=") || part == ri.value {
			found = i
			break
		}
	}

	if found > -1 {
		parts = append(parts[:found], parts[found+1:]...)
		dict.Set(ri.key, strings.Join(parts, ", "))
	}

}

func (ri *rwiKeyBasedDeleter) HasTokens() bool {
	return ri.hasTokens
}

type rwiPathSetter struct {
	value     string
	depth     int
	hasTokens bool
}

func (ri *rwiPathSetter) String() string {
	return fmt.Sprintf(`{"type":"pathSetter","value":"%s","depth":"%d","tokens":"%t"}`,
		ri.value, ri.depth, ri.hasTokens)
}

func (ri *rwiPathSetter) Parse(parts []string) error {
	pl := len(parts)
	if pl != 3 && pl != 4 {
		return errBadParams
	}
	ri.value = parts[2]

	if pl == 4 {
		v, err := strconv.ParseInt(parts[3], 10, 32)
		if err != nil {
			return errBadDepthParse
		}
		ri.depth = int(v)
	} else {
		ri.depth = -1
	}
	ri.hasTokens = checkTokens(ri.value)
	return nil
}

func (ri *rwiPathSetter) HasTokens() bool {
	return ri.hasTokens
}

func (ri *rwiPathSetter) Execute(r *http.Request) {
	if ri.depth > -1 {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= ri.depth {
			parts[ri.depth] = ri.value
			r.URL.Path = "/" + strings.Join(parts, "/")
		}
		return
	}

	if !strings.HasPrefix(ri.value, "/") {
		ri.value = "/" + ri.value
	}

	r.URL.Path = ri.value
}

type rwiPathReplacer struct {
	search, replacement string
	depth               int
	hasTokens           bool
}

func (ri *rwiPathReplacer) String() string {
	return fmt.Sprintf(
		`{"type":"pathReplacer","search":"%s","replacement":"%s","depth":"%d","tokens":"%t"}`,
		ri.search, ri.replacement, ri.depth, ri.hasTokens)
}

func (ri *rwiPathReplacer) Parse(parts []string) error {
	pl := len(parts)
	if pl != 4 && pl != 5 {
		return errBadParams
	}
	ri.search = parts[2]
	ri.replacement = parts[3]
	if pl == 5 {
		v, err := strconv.ParseInt(parts[4], 10, 32)
		if err != nil {
			return errBadDepthParse
		}
		ri.depth = int(v)
	} else {
		ri.depth = -1
	}
	ri.hasTokens = checkTokens(ri.search) || checkTokens(ri.replacement)
	return nil
}

func (ri *rwiPathReplacer) Execute(r *http.Request) {
	r.URL.Path = strings.Replace(r.URL.Path, ri.search, ri.replacement, ri.depth)
}

func (ri *rwiPathReplacer) HasTokens() bool {
	return ri.hasTokens
}

type rwiBasicSetter struct {
	value     string
	setter    scalarSetFunc
	getter    scalarGetFunc
	hasTokens bool
}

func (ri *rwiBasicSetter) String() string {
	return fmt.Sprintf(
		`{"type":"basicSetter","value":"%s","tokens":"%t"}`,
		ri.value, ri.hasTokens)
}

func (ri *rwiBasicSetter) Parse(parts []string) error {
	if len(parts) != 3 {
		return errBadParams
	}
	var ok bool
	if ri.setter, ok = scalarSets[parts[0]]; !ok {
		return errBadParams
	}
	ri.getter = scalarGets[parts[0]]
	ri.value = parts[2]
	ri.hasTokens = checkTokens(ri.value)
	return nil
}

func (ri *rwiBasicSetter) Execute(r *http.Request) {
	ri.setter(r, ri.value)
}

func (ri *rwiBasicSetter) HasTokens() bool {
	return ri.hasTokens
}

type rwiBasicReplacer struct {
	search, replacement string
	depth               int
	setter              scalarSetFunc
	getter              scalarGetFunc
	hasTokens           bool
}

func (ri *rwiBasicReplacer) String() string {
	return fmt.Sprintf(
		`{"type":"basicReplacer","search":"%s","replacement":"%s","depth":"%d","tokens":"%t"}`,
		ri.search, ri.replacement, ri.depth, ri.hasTokens)
}

func (ri *rwiBasicReplacer) Parse(parts []string) error {

	lp := len(parts)
	if lp != 4 && lp != 5 {
		return errBadParams
	}
	var ok bool
	if ri.setter, ok = scalarSets[parts[0]]; !ok {
		return errBadParams
	}
	ri.getter = scalarGets[parts[0]]

	ri.search = parts[2]
	ri.replacement = parts[3]
	if lp == 5 {
		v, err := strconv.ParseInt(parts[4], 10, 32)
		if err != nil {
			return errBadDepthParse
		}
		ri.depth = int(v)
	} else {
		ri.depth = -1
	}

	ri.hasTokens = checkTokens(ri.search) || checkTokens(ri.replacement)
	return nil
}

func (ri *rwiBasicReplacer) Execute(r *http.Request) {
	val := ri.getter(r)
	val = strings.Replace(val, ri.search, ri.replacement, ri.depth)
	ri.setter(r, val)
}

func (ri *rwiBasicReplacer) HasTokens() bool {
	return ri.hasTokens
}

type rwiPortDeleter struct {
}

func (ri *rwiPortDeleter) String() string {
	return `{"type":"portDeleter"}`
}

func (ri *rwiPortDeleter) Parse([]string) error {
	return nil
}

func (ri *rwiPortDeleter) Execute(r *http.Request) {
	if r != nil && r.URL != nil {
		h := r.URL.Host
		if i := strings.Index(h, ":"); i > 0 {
			h = h[:i]
		}
		r.URL.Host = h
	}
}

func (ri *rwiPortDeleter) HasTokens() bool {
	return false
}

type rwiChainExecutor struct {
	rewriterName string
	rewriter     RewriteInstructions
}

func (ri *rwiChainExecutor) String() string {
	return fmt.Sprintf(`{"type":"chainExecutor","rewriter":"%s"}`, ri.rewriterName)
}

func (ri *rwiChainExecutor) Parse(parts []string) error {
	lp := len(parts)
	if lp != 3 || strings.Trim(parts[2], " \t\n") == "" {
		return errBadParams
	}
	ri.rewriterName = parts[2]
	// a separate process will validate and map the rewriter based on this parsed name
	return nil
}

func (ri *rwiChainExecutor) Execute(r *http.Request) {
	if ri.rewriter == nil {
		return
	}

	// this incmements the RewriterHops counter for the request
	// and only executes the chained rewriter the counter is below the max allowed (32)
	h := context.IncrementedRewriterHops(r.Context(), 1)

	if h < options.MaxRewriterChainExecutions {
		ri.rewriter.Execute(r)
	}
}

func (ri *rwiChainExecutor) HasTokens() bool {
	return false
}
