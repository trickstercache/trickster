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
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	proxyurls "github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

type rewriteInstruction interface {
	String() string
	Parse([]string) error
	Execute(r *http.Request)
	HasTokens() bool
}

// RewriteInstructions is a list of type []rewriteInstruction
type RewriteInstructions []rewriteInstruction

// InstructionsLookup is a map of Options keyed by the RewriteInstructions Name
type InstructionsLookup map[string]RewriteInstructions

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

type (
	scalarGetFunc func(*http.Request) string
	scalarSetFunc func(*http.Request, string)
)

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
			proxyurls.SetUpstreamScheme(r, v)
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
			proxyurls.SetUpstreamHost(r, v)
		}
	},
	"hostname": func(r *http.Request, v string) {
		if r != nil && r.URL != nil {
			r.URL.Host = joinHostnamePort(v, r.URL.Port())
			proxyurls.SetUpstreamHostname(r, v)
		}
	},
	"port": func(r *http.Request, v string) {
		if r == nil || r.URL == nil {
			return
		}
		r.URL.Host = joinHostnamePort(r.URL.Hostname(), v)
		proxyurls.SetUpstreamPort(r, v)
	},
}

func joinHostnamePort(hostname, port string) string {
	hostname = strings.TrimPrefix(strings.TrimSuffix(hostname, "]"), "[")
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
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

// HasTokens returns true when an instruction consumes rewrite tokens.
func (ris RewriteInstructions) HasTokens() bool {
	for _, instr := range ris {
		if instr.HasTokens() {
			return true
		}
	}
	return false
}

func checkTokens(input string) bool {
	_, after, ok := strings.Cut(input, "${")
	return ok && strings.IndexByte(after, '}') > -1
}

// parseKeyBasedInstruction parses a 4-part key-based instruction
func parseKeyBasedInstruction(parts []string, dict *dictFunc, key *string, value *string, hasTokens *bool) error {
	if len(parts) != 4 {
		return errBadParams
	}
	var ok bool
	if *dict, ok = dicts[parts[0]]; !ok {
		return errBadParams
	}
	*key = parts[2]
	*value = parts[3]
	*hasTokens = checkTokens(*value)
	return nil
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
	return parseKeyBasedInstruction(parts, &ri.dict, &ri.key, &ri.value, &ri.hasTokens)
}

func (ri *rwiKeyBasedSetter) Execute(r *http.Request) {
	dict := ri.dict(r)
	value := ri.value
	if ri.hasTokens {
		value = expandTokens(r, value)
	}
	dict.Set(ri.key, value)
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
	return parseKeyBasedInstruction(parts, &ri.dict, &ri.key, &ri.value, &ri.hasTokens)
}

type mappable map[string][]string

func (ri *rwiKeyBasedAppender) Execute(r *http.Request) {
	dict := ri.dict(r)
	value := ri.value
	if ri.hasTokens {
		value = expandTokens(r, value)
	}
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
		dict.Set(ri.key, value)
		if q != nil {
			r.URL.RawQuery = q.Encode()
		}
		return
	}

	// appending to url param value
	if q != nil {
		if slices.Contains(vals, value) {
			// the desired value is already in the query, do nothing
			return
		}
		m[ri.key] = append(vals, value)
		r.URL.RawQuery = q.Encode()
		return
	}

	// appending to header value

	var subkey string
	j := strings.Index(value, "=")
	if j > 0 {
		subkey = value[:j]
	} else {
		subkey = value
	}

	// this might look redundant, but it normalizes something like:
	//  {"header": []string{"val1=abc, val2", "val3=def"}}
	// which should not happen but is technically possible
	parts := strings.Split(strings.Join(vals, ", "), ", ")

	var found bool
	for i, part := range parts {
		if part == value {
			// value exists in header already, nothing to do
			return
		}
		if strings.HasPrefix(part, subkey+"=") {
			// a right-subkey=wrong-value exists, set it to the right value
			parts[i] = value
			found = true
		}
	}

	if !found {
		parts = append(parts, value)
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
	key, search, replacement := ri.key, ri.search, ri.replacement
	if ri.hasTokens {
		key = expandTokens(r, key)
		search = expandTokens(r, search)
		replacement = expandTokens(r, replacement)
	}
	depth := ri.depth
	if depth == 0 {
		depth = -1
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

	vals, ok = m[key]
	if !ok {
		return
	}

	for i := range vals {
		vals[i] = strings.Replace(vals[i], search, replacement, depth)
	}
	m[key] = vals

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
	key, value := ri.key, ri.value
	if ri.hasTokens {
		key = expandTokens(r, key)
		value = expandTokens(r, value)
	}

	if value == "" {
		dict.Del(key)
		if qp, ok := dict.(url.Values); ok {
			r.URL.RawQuery = qp.Encode()
		}
		return
	}

	found := -1
	// url params
	if qp, ok := dict.(url.Values); ok {
		if vals, ok1 := qp[key]; ok1 {
			for i, v := range vals {
				if v == value {
					found = i
					break
				}
			}
			if found > -1 {
				qp[key] = append(vals[:found], vals[found+1:]...)
				r.URL.RawQuery = qp.Encode()
			}
		}
		return
	}

	// headers
	val := dict.Get(key)
	parts := strings.Split(val, ", ")
	for i, part := range parts {
		if strings.HasPrefix(part, value+"=") || part == value {
			found = i
			break
		}
	}

	if found > -1 {
		parts = append(parts[:found], parts[found+1:]...)
		dict.Set(key, strings.Join(parts, ", "))
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
	value := ri.value
	if ri.hasTokens {
		value = expandTokens(r, value)
	}
	if ri.depth > -1 {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= ri.depth {
			parts[ri.depth] = value
			r.URL.Path = "/" + strings.Join(parts, "/")
		}
		return
	}

	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}

	r.URL.Path = value
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
	search, replacement := ri.search, ri.replacement
	if ri.hasTokens {
		search = expandTokens(r, search)
		replacement = expandTokens(r, replacement)
	}
	r.URL.Path = strings.Replace(r.URL.Path, search, replacement, ri.depth)
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
	value := ri.value
	if ri.hasTokens {
		value = expandTokens(r, value)
	}
	ri.setter(r, value)
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
	search, replacement := ri.search, ri.replacement
	if ri.hasTokens {
		search = expandTokens(r, search)
		replacement = expandTokens(r, replacement)
	}
	current := ri.getter(r)
	value := strings.Replace(current, search, replacement, ri.depth)
	if value != current {
		ri.setter(r, value)
	}
}

func (ri *rwiBasicReplacer) HasTokens() bool {
	return ri.hasTokens
}

type rwiPortDeleter struct{}

func (ri *rwiPortDeleter) String() string {
	return `{"type":"portDeleter"}`
}

func (ri *rwiPortDeleter) Parse([]string) error {
	return nil
}

func (ri *rwiPortDeleter) Execute(r *http.Request) {
	if r != nil && r.URL != nil {
		r.URL.Host = joinHostnamePort(r.URL.Hostname(), "")
		proxyurls.SetUpstreamPort(r, "")
	}
}

func (ri *rwiPortDeleter) HasTokens() bool {
	return false
}

type rwiChainExecutor struct {
	rewriterName string
	rewriter     RewriteInstructions
	hasTokens    bool
}

func (ri *rwiChainExecutor) String() string {
	return fmt.Sprintf(`{"type":"chainExecutor","rewriter":"%s"}`, ri.rewriterName)
}

func (ri *rwiChainExecutor) Parse(parts []string) error {
	lp := len(parts)
	if lp != 3 || strings.TrimSpace(parts[2]) == "" {
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
	return ri.hasTokens
}
