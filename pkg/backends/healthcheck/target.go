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

package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// target defines a Health Check target
type target struct {
	name                  string
	description           string
	baseRequest           *http.Request
	httpClient            *http.Client
	interval              time.Duration
	status                *Status
	failureThreshold      int
	recoveryThreshold     int
	failConsecutiveCnt    atomic.Int32
	successConsecutiveCnt atomic.Int32
	ks                    int // used internally and is not thread safe, do not expose
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
	ceb                   bool
	eb                    string
	eh                    http.Header
	ec                    []int
}

// DemandProbe defines a health check probe that makes an HTTP Request to the backend and writes the
// response to the provided ResponseWriter
type DemandProbe func(w http.ResponseWriter)

// newTarget returns a new target
func newTarget(_ context.Context, name, description string, o *ho.Options,
	client *http.Client) (*target, error) {

	if o == nil {
		return nil, ho.ErrNoOptionsProvided
	}
	var rd io.Reader
	if o.Body != "" {
		rd = strings.NewReader(o.Body)
	}
	r, err := http.NewRequest(o.Verb, o.URL().String(), rd)
	if err != nil {
		return nil, err
	}
	if len(o.Headers) > 0 {
		r.Header = headers.Lookup(o.Headers).ToHeader()
	}
	interval := o.Interval
	if client == nil {
		client = newHTTPClient(ho.CalibrateTimeout(o.Timeout))
	}
	if o.FailureThreshold < 1 {
		o.FailureThreshold = 3 // default to 3
	}
	if o.RecoveryThreshold < 1 {
		o.RecoveryThreshold = 3 // default to 3
	}
	isd := fmt.Sprintf("unknown monitored status (check interval: %dms)", interval.Milliseconds())
	t := &target{
		name:              name,
		description:       description,
		baseRequest:       r,
		httpClient:        client,
		failureThreshold:  o.FailureThreshold,
		recoveryThreshold: o.RecoveryThreshold,
		interval:          interval,
	}
	t.status = &Status{name: name, detail: isd, description: description, prober: t.demandProbe}
	if len(o.ExpectedHeaders) > 0 {
		t.eh = headers.Lookup(o.ExpectedHeaders).ToHeader()
	}
	if o.HasExpectedBody() {
		t.ceb = true
		t.eb = o.ExpectedBody
	}
	if len(o.ExpectedCodes) > 0 {
		t.ec = o.ExpectedCodes
	} else {
		t.ec = []int{200}
	}
	return t, nil
}

func (t *target) isGoodHeader(h http.Header) bool {
	if len(t.eh) == 0 {
		return true
	}
	if len(h) == 0 {
		t.status.detail = "no response headers"
		return false
	}
	for k := range t.eh {
		if _, ok := h[k]; !ok {
			t.status.detail = fmt.Sprintf("server response is missing required header [%s]", k)
			return false
		}
		if t.eh.Get(k) != h.Get(k) {
			t.status.detail = fmt.Sprintf("required header mismatch for [%s] got [%s] expected [%s]",
				k, h.Get(k), t.eh.Get(k))
			return false
		}
	}
	return true
}

func (t *target) isGoodCode(i int) bool {
	for _, v := range t.ec {
		if v == i {
			return true
		}
	}
	t.status.detail = fmt.Sprintf("required status code mismatch, got [%d] expected one of %v", i, t.ec)
	return false
}

func (t *target) isGoodBody(r io.ReadCloser) bool {
	if !t.ceb {
		return true
	}
	x, err := io.ReadAll(r)
	if err != nil {
		t.status.detail = "error reading response body from target"
		return false
	}
	if string(x) != t.eb {
		t.status.detail = fmt.Sprintf("required response body mismatch expected [%s] got [%s]", t.eb, string(x))
		return false
	}
	return true
}

// Start begins health checking the target
func (t *target) Start(ctx context.Context) {
	if t.cancel != nil {
		t.Stop()
	}
	ctx, cancel := context.WithCancel(tctx.WithHealthCheckFlag(ctx, true))
	t.cancel = cancel
	t.probeLoop(ctx)
}

// Stop stops healthchecking the target
func (t *target) Stop() {
	if t.cancel == nil {
		return
	}
	t.cancel()
	t.wg.Wait()
}

func (t *target) probeLoop(ctx context.Context) {
	t.wg.Add(1)
	t.probe(ctx) // perform initial probe
	ticker := time.NewTicker(t.interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				t.wg.Done()
				ticker.Stop()
				return // probe complete, stop loop and prevent goroutine leak
			case <-ticker.C:
				t.probe(ctx)
			}
		}
	}()
}

func (t *target) probe(ctx context.Context) {
	r := t.baseRequest.Clone(ctx)
	resp, err := t.httpClient.Do(r)
	var errCnt, successCnt int
	var passed bool
	switch {
	case err != nil, resp == nil:
		t.status.detail = fmt.Sprintf("error probing target: %v", err)
		errCnt = int(t.failConsecutiveCnt.Add(1))
		t.successConsecutiveCnt.Store(0)
	case !t.isGoodCode(resp.StatusCode) || !t.isGoodHeader(resp.Header) || !t.isGoodBody(resp.Body):
		errCnt = int(t.failConsecutiveCnt.Add(1))
		t.successConsecutiveCnt.Store(0)
	default:
		resp.Body.Close()
		successCnt = int(t.successConsecutiveCnt.Add(1))
		t.failConsecutiveCnt.Store(0)
		passed = true
	}
	if !passed && t.ks != -1 && (errCnt == t.failureThreshold || t.ks == 0) {
		t.status.failingSince = time.Now()
		t.status.Set(-1)
		t.ks = -1
		logger.Info("hc status changed",
			logging.Pairs{"targetName": t.name, "status": "failed",
				"detail": t.status.detail, "threshold": t.failureThreshold})
	} else if passed && t.ks != 1 && (successCnt == t.recoveryThreshold || t.ks == 0) {
		t.status.failingSince = time.Time{}
		t.status.Set(1)
		t.ks = 1
		t.status.detail = "" // this is only populated with failure details, so it is cleared upon recovery
		logger.Info("hc status changed",
			logging.Pairs{"targetName": t.name, "status": "available",
				"threshold": t.recoveryThreshold})
	}
}

func (t *target) demandProbe(w http.ResponseWriter) {
	r := t.baseRequest.Clone(context.Background())
	resp, err := t.httpClient.Do(r)
	h := w.Header()
	if err != nil {
		if t.status != nil && t.status.Get() != 0 {
			sh := t.status.Headers()
			for k := range sh {
				h.Set(k, sh.Get(k))
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error performing health check: " + err.Error()))
		return
	}
	for k := range resp.Header {
		h.Set(k, resp.Header.Get(k))
	}
	if t.status != nil && t.status.Get() != 0 {
		sh := t.status.Headers()
		for k := range sh {
			h.Set(k, sh.Get(k))
		}
	}
	w.WriteHeader(resp.StatusCode)
	if resp.Body != nil {
		io.Copy(w, resp.Body)
	}
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: 5 * time.Second}).Dial,
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 32,
		},
	}
}
