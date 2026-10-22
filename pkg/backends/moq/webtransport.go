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

package moq

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	listenerhttp3 "github.com/trickstercache/trickster/v2/pkg/proxy/listener/http3"

	"github.com/dunglas/httpsfv"
	"github.com/quic-go/webtransport-go"
)

const (
	transportWebTransport = "webtransport"

	msgNotWebTransport = "a moq backend accepts only WebTransport sessions over HTTP/3"
	msgOriginRefused   = "origin not allowed"
	msgNoDraft         = "no supported MoQ Transport draft offered in " + listenerhttp3.HeaderWTAvailableProtocols
	msgBusy            = "session limit reached"
	msgUpgradeFailed   = "WebTransport session could not be established"
)

func (c *Client) serveWebTransport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		w.Header().Set(headers.NameAllow, http.MethodConnect)
		http.Error(w, msgNotWebTransport, http.StatusMethodNotAllowed)
		return
	}
	listenerName, ok := listenerhttp3.WebTransportRequest(r)
	if !ok {
		http.Error(w, msgNotWebTransport, http.StatusBadRequest)
		return
	}
	backend := c.Name()
	o := c.webTransportOptions()
	if !originAllowed(r, o.AllowedOrigins) {
		// draft-ietf-webtrans-http3 section 3.1: a refused origin is answered with 403
		observeSession(listenerName, backend, transportWebTransport, "", resultRefused)
		http.Error(w, msgOriginRefused, http.StatusForbidden)
		return
	}
	cd, protocol, ok := selectDraft(r.Header, o)
	if !ok {
		observeSession(listenerName, backend, transportWebTransport, "", resultRefused)
		http.Error(w, msgNoDraft, http.StatusBadRequest)
		return
	}
	draft := strconv.Itoa(cd.Draft())
	if !c.sh.admitWebTransport(o.MaxSessions) {
		observeSession(listenerName, backend, transportWebTransport, draft, resultBusy)
		http.Error(w, msgBusy, http.StatusServiceUnavailable)
		return
	}
	defer c.sh.wtActive.Add(-1)
	wts, err := listenerhttp3.UpgradeWebTransport(r, protocol)
	if err != nil {
		observeSession(listenerName, backend, transportWebTransport, draft, resultFailed)
		http.Error(w, msgUpgradeFailed, http.StatusBadRequest)
		return
	}
	// the relay outlives a reload that retires it while this session uses it
	c.sh.hold()
	defer c.sh.drop()
	client := tctx.ClientIP(r.Context())
	if client == "" {
		client = clientip.PeerIP(r.RemoteAddr)
	}
	addr, _ := netip.ParseAddr(client)
	handler := &leavable{Handler: limitRequests(c.Relay().Handler(c.Scope()), c.limiter, addr)}
	sess, err := session.New(session.FromWebTransport(wts), session.Options{
		Role: session.RoleServer, Codec: cd, WebTransport: true, Handler: handler,
		HandshakeTimeout: mo.DefaultHandshakeTimeout, MaxRequests: mo.DefaultMaxRequestsPerSession,
		Setup: message.Setup{
			Implementation: implementation(""), MaxFilterRanges: mo.DefaultMaxFilterRanges,
			MaxRequestUpdates: mo.DefaultMaxRequestUpdates,
		},
	})
	if err != nil {
		_ = wts.CloseWithError(webtransport.SessionErrorCode(message.CodeInternalError), "")
		observeSession(listenerName, backend, transportWebTransport, draft, resultFailed)
		return
	}
	// the session outlives the request's context, which ends with the CONNECT stream
	if err = sess.Start(context.WithoutCancel(r.Context())); err != nil {
		observeSession(listenerName, backend, transportWebTransport, draft, resultFailed)
		return
	}
	c.sh.trackWT(sess, addr)
	defer c.sh.untrackWT(sess)
	start := time.Now()
	observeSession(listenerName, backend, transportWebTransport, draft, resultOK)
	activeSessions(listenerName, backend, 1)
	if logger.DebugEnabled() {
		logger.Debug("moq session opened", logging.Pairs{
			keys.ListenerName: listenerName, keys.BackendName: backend, keys.Draft: draft, logKeyClient: client,
			keys.Transport: transportWebTransport,
		})
	}
	select {
	case <-sess.Done():
	case <-listenerhttp3.Draining(r):
		// the listener is stopping: the client is asked to move, and the session closes once it has or the listener
		// gives up waiting
		handler.leave(o.GoAwayURI)
		_ = sess.GoAway(o.GoAwayURI, 0)
		<-sess.Done()
	}
	activeSessions(listenerName, backend, -1)
	logger.Info("moq session closed", logging.Pairs{
		keys.ListenerName: listenerName, keys.BackendName: backend, keys.Draft: draft, logKeyClient: client,
		keys.Transport: transportWebTransport, logKeyDuration: time.Since(start).String(),
		logKeyReason: sessionEnd(sess.Err()),
	})
}

func (c *Client) webTransportOptions() *mo.WebTransportOptions {
	if o := c.Configuration(); o != nil && o.MoQ != nil && o.MoQ.WebTransport != nil {
		return o.MoQ.WebTransport
	}
	return mo.New().WebTransport
}

func originAllowed(r *http.Request, allowed []string) bool {
	// draft-ietf-webtrans-http3 section 3.1: an Origin, which browsers always send, must be one the server allows
	origin := r.Header.Get(headers.NameOrigin)
	if origin == "" {
		return true
	}
	if len(allowed) == 0 {
		u, err := url.Parse(origin)
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
	}
	if slices.Contains(allowed, mo.AnyOrigin) {
		return true
	}
	normalized, err := mo.NormalizeOrigin(origin)
	return err == nil && slices.Contains(allowed, normalized)
}

func selectDraft(h http.Header, o *mo.WebTransportOptions) (*codec.Codec, string, bool) {
	// the backend's preference decides among the drafts the client offers; a client offering none gets the
	// configured default draft, if any, and no WT-Protocol in the response
	offered := offeredProtocols(h[http.CanonicalHeaderKey(listenerhttp3.HeaderWTAvailableProtocols)])
	if offered == nil {
		if o.DefaultDraft == 0 {
			return nil, "", false
		}
		cd, ok := codec.ForDraft(o.DefaultDraft)
		return cd, "", ok
	}
	for _, d := range o.Drafts {
		if cd, ok := codec.ForDraft(d); ok && slices.Contains(offered, cd.ALPN()) {
			return cd, cd.ALPN(), true
		}
	}
	return nil, "", false
}

func offeredProtocols(values []string) []string {
	// a list that does not parse offers nothing usable; an absent or empty one offers nothing at all
	if len(values) == 0 {
		return nil
	}
	list, err := httpsfv.UnmarshalList(values)
	if err != nil {
		return []string{}
	}
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, member := range list {
		if item, ok := member.(httpsfv.Item); ok {
			if s, ok := item.Value.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
