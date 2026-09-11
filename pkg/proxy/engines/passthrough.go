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
	"bytes"
	"io"
	"math"
	"net/http"
	"net/http/httputil"
	"strconv"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"

	"golang.org/x/net/http/httpguts"
)

var passthroughBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, HTTPBlockSize)
		return &b
	},
}

type bufferPool struct{}

func (bufferPool) Get() []byte {
	return *passthroughBuffers.Get().(*[]byte)
}

func (bufferPool) Put(b []byte) {
	if cap(b) != HTTPBlockSize {
		return
	}
	b = b[:HTTPBlockSize]
	passthroughBuffers.Put(&b)
}

// NewPassthroughHandler returns a Handler that proxies to the backend without
// caching. It delegates to net/http/httputil.ReverseProxy so that protocol
// upgrades, response trailers, 1xx relay and streaming flush behavior come
// from the standard library instead of being reimplemented here.
//
// The backend's Transport is reused rather than its Client, which intentionally
// drops Client.Timeout: that timeout bounds the entire body read and would
// truncate long-lived streams and large objects.
func NewPassthroughHandler(client backends.Backend) http.Handler {
	var rt http.RoundTripper
	if c := client.HTTPClient(); c != nil {
		rt = c.Transport
	}
	if o := client.Configuration(); o != nil && rt != nil {
		rt = &idleTimeoutTransport{next: rt, timeout: time.Duration(o.Timeout)}
	}
	return &httputil.ReverseProxy{
		Transport:      rt,
		Rewrite:        passthroughRewrite(client),
		ModifyResponse: passthroughModifyResponse,
		ErrorHandler:   passthroughErrorHandler,
		BufferPool:     bufferPool{},
	}
}

func passthroughRewrite(client backends.Backend) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		r := pr.Out
		rsc := request.GetResources(r)

		// built from the inbound request so the original query survives:
		// ReverseProxy runs cleanQueryParams over the outbound RawQuery first,
		// which drops params containing ';' or a malformed percent-escape
		if base := client.BaseUpstreamURL(); base != nil {
			r.URL = urls.BuildUpstreamURL(pr.In, base)
		}

		o := client.Configuration()
		if rsc != nil && rsc.BackendOptions != nil {
			o = rsc.BackendOptions
		}
		// ReverseProxy has already put back the two hop-by-hop headers it needs
		// on the outbound request: Te: trailers, and the Connection/Upgrade
		// pair for a tunnel. AddForwardingHeaders strips all of them again as
		// hop-by-hop, so capture and restore them around it -- otherwise an
		// upgrade is silently downgraded and trailers never reach the origin.
		wantsTrailers := httpguts.HeaderValuesContainsToken(pr.In.Header[headers.NameTe], "trailers")
		upgradeType := r.Header.Get(headers.NameUpgrade)
		isUpgrade := upgradeType != "" &&
			httpguts.HeaderValuesContainsToken(r.Header[headers.NameConnection], "Upgrade")

		if o != nil {
			headers.AddForwardingHeaders(r, o.ForwardedHeaders)
		}
		if wantsTrailers {
			r.Header.Set(headers.NameTe, "trailers")
		}
		if isUpgrade {
			r.Header.Set(headers.NameConnection, "Upgrade")
			r.Header.Set(headers.NameUpgrade, upgradeType)
		}
		// clear the Host header or it is forwarded upstream
		r.Host = ""

		if rsc != nil {
			if pc := rsc.PathConfig; pc != nil {
				if len(pc.RequestHeaders) > 0 {
					headers.UpdateRequestHeaders(r, pc.RequestHeaders)
				}
				if len(pc.RequestParams) > 0 {
					qp, _, _ := params.GetRequestValues(r)
					params.UpdateParams(qp, pc.RequestParams)
					params.SetRequestValues(r, qp)
				}
			}
			// W3C returns a request sharing this one's header map, so the
			// injected trace headers land on r even though the copy is dropped
			if rsc.Tracer != nil {
				tspan.PrepareOutgoingRequest(r.Context(), r, rsc.Tracer)
			}
		}

		if ep := profile.FromContext(r.Context()); ep != nil && ep.SupportedHeaderVal != "" {
			r.Header.Set(headers.NameAcceptEncoding, ep.SupportedHeaderVal)
		}
	}
}

func passthroughModifyResponse(resp *http.Response) error {
	if resp == nil {
		return nil
	}
	r := resp.Request
	var rsc *request.Resources
	if r != nil {
		rsc = request.GetResources(r)
	}

	// a 101 is a tunnel handshake, not a proxied response: its Connection and
	// Upgrade headers are the payload and must survive to the client
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return nil
	}

	if rsc != nil {
		if ep := profile.FromContext(r.Context()); ep != nil {
			if ce := resp.Header.Get(headers.NameContentEncoding); ce != "" {
				ep.ContentEncoding = ce
			}
		}
		if pc := rsc.PathConfig; pc != nil {
			if len(pc.ResponseHeaders) > 0 {
				headers.UpdateHeaders(resp.Header, pc.ResponseHeaders)
			}
			if pc.ResponseBodyBytes != nil {
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(pc.ResponseBodyBytes))
				resp.ContentLength = int64(len(pc.ResponseBodyBytes))
				resp.Header.Set(headers.NameContentLength,
					strconv.Itoa(len(pc.ResponseBodyBytes)))
			}
		}
		if o := rsc.BackendOptions; o != nil {
			warnOnClockOffset(o.Name, resp.Header)
		}
	}

	setStatusHeader(resp.StatusCode, resp.Header)
	// matches the request-side strip in AddForwardingHeaders; ReverseProxy's
	// own hop-header removal does not cover Accept-Encoding
	headers.StripClientHeaders(resp.Header)
	return nil
}

func passthroughErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	var name, provider string
	if rsc := request.GetResources(r); rsc != nil && rsc.BackendOptions != nil {
		name = rsc.BackendOptions.Name
		provider = rsc.BackendOptions.Provider
	}
	logger.Error("error reaching upstream origin",
		logging.Pairs{
			keys.URL:             r.URL.String(),
			keys.BackendName:     name,
			keys.BackendProvider: provider,
			keys.Detail:          err.Error(),
		})
	h := w.Header()
	headers.SetResultsHeader(h, "HTTPProxy", status.LookupStatusProxyError.String(), "", nil, nil)
	w.WriteHeader(http.StatusBadGateway)
}

func warnOnClockOffset(backendName string, h http.Header) {
	date := h.Get(headers.NameDate)
	if date == "" {
		return
	}
	d, err := http.ParseTime(date)
	if err != nil {
		return
	}
	offset := time.Since(d)
	if time.Duration(math.Abs(float64(offset))) <= time.Minute {
		return
	}
	logger.WarnOnce("clockoffset."+backendName,
		ClockOffsetWarning,
		logging.Pairs{
			keys.BackendName: backendName,
			"tricksterTime":  strconv.FormatInt(d.Add(offset).Unix(), 10),
			"originTime":     strconv.FormatInt(d.Unix(), 10),
			"offset":         strconv.FormatInt(int64(offset.Seconds()), 10) + "s",
		})
}

// idleTimeoutTransport bounds a stalled response body without capping the
// total transfer, which is what a streaming or large-object lane needs.
type idleTimeoutTransport struct {
	next    http.RoundTripper
	timeout time.Duration
}

func (t *idleTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(r)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	// a switched protocol hands the body to the tunnel as a ReadWriteCloser;
	// wrapping it would both break that assertion and time out an idle tunnel
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return resp, nil
	}
	resp.Body = newIdleTimeoutBody(resp.Body, t.timeout)
	return resp, nil
}
