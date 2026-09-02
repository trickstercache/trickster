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

package engines

import (
	"context"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"golang.org/x/net/http/httpguts"
)

// collapseEligible reports whether a response may be fanned out to multiple
// clients. Collapsing delivers identical bytes to everyone who joins, which is
// strictly stronger than caching, so anything RFC 9111 forbids a shared cache
// from storing must also refuse to collapse. Default is to refuse: a missed
// collapse costs a fetch; a wrong one is cross-user disclosure.
func collapseEligible(r *http.Request, statusCode int, h http.Header, pc *po.Options) bool {
	if statusCode != http.StatusOK {
		return false
	}
	if h.Get(headers.NameSetCookie) != "" {
		return false
	}
	if base, _, err := mime.ParseMediaType(h.Get(headers.NameContentType)); err == nil &&
		base == ContentTypeEventStream {
		return false
	}
	var isPublic bool
	for _, cc := range h.Values(headers.NameCacheControl) {
		for tok := range strings.SplitSeq(cc, ",") {
			tok = strings.ToLower(strings.TrimSpace(tok))
			d, _, _ := strings.Cut(tok, "=")
			switch d {
			case headers.ValuePrivate, headers.ValueNoStore:
				return false
			case headers.ValuePublic, headers.ValueSharedMaxAge, headers.ValueMustRevalidate:
				isPublic = true
			}
		}
	}
	// RFC 9111 3.5: an authorized response requires explicit shared-cache
	// permission before it may be reused for anyone else
	if r.Header.Get(headers.NameAuthorization) != "" && !isPublic {
		return false
	}
	// a Vary field not reflected in the collapse key means two joiners could
	// legitimately deserve different bytes
	for _, vv := range h.Values(headers.NameVary) {
		for f := range strings.SplitSeq(vv, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if f == "*" || pc == nil || !slices.ContainsFunc(pc.CacheKeyHeaders,
				func(k string) bool { return strings.EqualFold(k, f) }) {
				return false
			}
		}
	}
	return true
}

func requestIsUpgrade(r *http.Request) bool {
	return r.Header.Get(headers.NameUpgrade) != "" &&
		httpguts.HeaderValuesContainsToken(r.Header[headers.NameConnection], "Upgrade")
}

// collapses tracks in-flight Lane A collapse sessions by cache key.
var collapses sync.Map

type collapseEntry struct {
	cond     *sync.Cond
	resolved bool
	pcf      ProgressiveCollapseForwarder // nil at resolution = fetch independently
}

func newCollapseEntry() *collapseEntry {
	return &collapseEntry{cond: sync.NewCond(&sync.Mutex{})}
}

// await blocks until the leader learns whether the response is collapsible,
// returning the PCF to join, or nil to fetch independently.
func (e *collapseEntry) await() ProgressiveCollapseForwarder {
	e.cond.L.Lock()
	for !e.resolved {
		e.cond.Wait()
	}
	pcf := e.pcf
	e.cond.L.Unlock()
	return pcf
}

func (e *collapseEntry) resolve(pcf ProgressiveCollapseForwarder) {
	e.cond.L.Lock()
	if !e.resolved {
		e.resolved = true
		e.pcf = pcf
	}
	e.cond.Broadcast()
	e.cond.L.Unlock()
}

// CollapsedPassthrough wraps a passthrough handler with Progressive Collapsed
// Forwarding: concurrent requests for the same key share one upstream fetch.
// Eligibility is only knowable once the upstream's headers arrive, so the
// leader fetches optimistically and followers wait on the pending entry.
func CollapsedPassthrough(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc == nil || rsc.BackendOptions == nil ||
			!methods.IsCacheable(r.Method) || requestIsUpgrade(r) {
			inner.ServeHTTP(w, r)
			return
		}
		o := rsc.BackendOptions
		pr := newProxyRequest(r, w)
		key := ComposeCacheKey(o.Name, o.CacheKeyPrefix, "", pr.DeriveCacheKey(""))

		actual, loaded := collapses.LoadOrStore(key, newCollapseEntry())
		e := actual.(*collapseEntry)
		if !loaded {
			leadCollapse(e, key, inner, w, r, int64(o.MaxObjectSizeBytes))
			return
		}
		pcf := e.await()
		if pcf == nil {
			inner.ServeHTTP(w, r)
			return
		}
		joinCollapse(pcf, w, r)
	})
}

func joinCollapse(pcf ProgressiveCollapseForwarder, w http.ResponseWriter, r *http.Request) {
	resp := pcf.GetResp()
	writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
	if err := pcf.AddClient(streamWriter(writer, resp)); err != nil {
		logger.Error("collapsed client stream failed",
			logging.Pairs{keys.URL: r.URL.String(), keys.Error: err.Error()})
		abortOnCopyError(w, r, err)
	}
}

// leadCollapse runs the upstream fetch on a goroutine detached from this
// client's cancellation, so a leader disconnect does not tear down the stream
// for followers; the fetch runs to completion once started.
func leadCollapse(e *collapseEntry, key string, inner http.Handler,
	w http.ResponseWriter, r *http.Request, maxSize int64,
) {
	rsc := request.GetResources(r)
	var pc *po.Options
	if rsc != nil {
		pc = rsc.PathConfig
	}
	cw := &collapseCapture{
		entry:   e,
		key:     key,
		req:     r,
		pc:      pc,
		leader:  w,
		maxSize: maxSize,
		header:  make(http.Header),
	}
	lr := r.WithContext(context.WithoutCancel(r.Context()))
	var wg sync.WaitGroup
	wg.Add(1)
	goWithRecover("collapse.leader", func() {
		defer wg.Done()
		defer collapses.CompareAndDelete(key, e)
		defer cw.finish()
		inner.ServeHTTP(cw, lr)
	})
	if pcf := e.await(); pcf != nil {
		joinCollapse(pcf, w, r)
	}
	// in tee mode the capture writes into w directly, which is only valid
	// while this handler is still on the stack
	wg.Wait()
}

// collapseCapture is the ResponseWriter handed to the detached leader fetch.
// WriteHeader is the moment eligibility becomes decidable: an eligible
// response feeds a PCF that followers join; anything else streams straight
// through to the leader's own client with no buffering.
type collapseCapture struct {
	entry    *collapseEntry
	key      string
	req      *http.Request
	pc       *po.Options
	leader   http.ResponseWriter
	maxSize  int64
	header   http.Header
	pcf      ProgressiveCollapseForwarder
	tee      io.Writer
	written  int64
	declared int64
	closed   bool
}

func (c *collapseCapture) Header() http.Header { return c.header }

func (c *collapseCapture) WriteHeader(code int) {
	// 1xx responses are informational and do not resolve eligibility; they
	// cannot be relayed through a collapse, so they are dropped here
	if code < http.StatusOK {
		return
	}
	if c.pcf != nil || c.tee != nil {
		return
	}
	c.declared = -1
	if v := c.header.Get(headers.NameContentLength); v != "" {
		if cl, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.declared = cl
		}
	}
	if collapseEligible(c.req, code, c.header, c.pc) &&
		(c.declared < 0 || c.declared < c.maxSize) {
		resp := &http.Response{
			StatusCode:    code,
			Header:        c.header.Clone(),
			ContentLength: c.declared,
			Request:       c.req,
		}
		if pcf := NewPCF(resp, c.declared, c.maxSize); pcf != nil {
			c.pcf = pcf
			c.entry.resolve(pcf)
			return
		}
	}
	// not collapsible: release followers to fetch independently and deliver
	// this response only to the leader, unbuffered
	c.entry.resolve(nil)
	collapses.CompareAndDelete(c.key, c.entry)
	writer := PrepareResponseWriter(c.leader, code, c.header)
	c.tee = streamWriter(writer, &http.Response{
		StatusCode: code, Header: c.header, ContentLength: c.declared,
	})
}

func (c *collapseCapture) Write(b []byte) (int, error) {
	if c.pcf == nil && c.tee == nil {
		c.WriteHeader(http.StatusOK)
	}
	var n int
	var err error
	if c.pcf != nil {
		n, err = c.pcf.Write(b)
		if err != nil {
			c.closed = true
			c.pcf.CloseWithError(err)
			logger.Error("collapsed upstream write failed",
				logging.Pairs{keys.Key: c.key, keys.Error: err.Error()})
		}
	} else {
		n, err = c.tee.Write(b)
	}
	c.written += int64(n)
	return n, err
}

// Flush satisfies ReverseProxy's streaming path; PCF distribution provides its
// own pacing, and the tee writer flushes per-write already.
func (c *collapseCapture) Flush() {}

// finish closes out the collapse after the detached fetch returns, converting
// a short read into an error every attached client observes.
func (c *collapseCapture) finish() {
	c.entry.resolve(nil) // no-op unless the fetch died before WriteHeader
	if c.pcf == nil || c.closed {
		return
	}
	if c.req.Method != http.MethodHead && c.declared > 0 && c.written < c.declared {
		c.pcf.CloseWithError(io.ErrUnexpectedEOF)
		logger.Error("collapsed upstream returned short body",
			logging.Pairs{
				keys.Key:   c.key,
				"declared": strconv.FormatInt(c.declared, 10),
				"received": strconv.FormatInt(c.written, 10),
			})
		return
	}
	c.pcf.Close()
}
