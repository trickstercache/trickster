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

package relay_test

import (
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func originNamespaces(t *testing.T, opts relay.Options) *harness {
	t.Helper()
	h := newHarness(t, opts, nil)
	h.o.namespaces = make(chan *session.Request, 8)
	return h
}

func listen(t *testing.T, c *session.Session, prefix wire.Namespace) (*session.Request, *rec) {
	t.Helper()
	r := newRec()
	req, err := c.Open(context.Background(), &message.SubscribeNamespace{Prefix: prefix}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, req).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_NAMESPACE refused")
	}
	return req, r
}

func expectNamespace(t *testing.T, r *rec, suffix wire.Namespace) {
	t.Helper()
	if n, ok := recv(t, r.messages, "NAMESPACE").(*message.Namespace); !ok || !n.Suffix.Equal(suffix) {
		t.Fatalf("got %+v, want NAMESPACE %v", n, suffix)
	}
}

func expectNamespaceDone(t *testing.T, r *rec, suffix wire.Namespace) {
	t.Helper()
	if d, ok := recv(t, r.messages, "NAMESPACE_DONE").(*message.NamespaceDone); !ok || !d.Suffix.Equal(suffix) {
		t.Fatalf("got %+v, want NAMESPACE_DONE %v", d, suffix)
	}
}

func expectQuiet(t *testing.T, r *rec) {
	t.Helper()
	select {
	case m := <-r.messages:
		t.Fatalf("unexpected %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNamespacesFromOrigin(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	_, first := listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	if m, ok := up.Message().(*message.SubscribeNamespace); !ok || !m.Prefix.Equal(ns("live")) {
		t.Fatalf("upstream asked for %+v", up.Message())
	}
	if err := up.Send(&message.Namespace{Suffix: ns("studio")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, first, ns("studio"))

	// a second listener of the prefix shares the origin subscription and learns what it already reported
	_, second := listen(t, h.client(), ns("live"))
	expectNamespace(t, second, ns("studio"))
	if n := h.o.nsAsks.Load(); n != 1 {
		t.Errorf("the origin was asked %d times", n)
	}
	// a listener of a longer prefix shares it too, since one session cannot carry overlapping prefixes; the
	// reported namespace that equals its prefix comes with an empty suffix
	_, deep := listen(t, h.client(), ns("live", "studio"))
	expectNamespace(t, deep, ns())
	if err := up.Send(&message.Namespace{Suffix: ns("studio", "cam1")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, deep, ns("cam1"))
	expectNamespace(t, first, ns("studio", "cam1"))
	expectNamespace(t, second, ns("studio", "cam1"))
	if n := h.o.nsAsks.Load(); n != 1 {
		t.Errorf("the origin was asked %d times", n)
	}

	if err := up.Send(&message.NamespaceDone{Suffix: ns("studio")}); err != nil {
		t.Fatal(err)
	}
	expectNamespaceDone(t, first, ns("studio"))
	expectNamespaceDone(t, second, ns("studio"))
	expectNamespaceDone(t, deep, ns())
	expectQuiet(t, deep)
}

func TestWiderPrefixReplacesNarrower(t *testing.T) {
	h := originNamespaces(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond})
	_, deep := listen(t, h.client(), ns("live", "studio"))
	narrow := recv(t, h.o.namespaces, "the narrow SUBSCRIBE_NAMESPACE")
	for _, cam := range []string{"cam1", "cam2"} {
		if err := narrow.Send(&message.Namespace{Suffix: ns(cam)}); err != nil {
			t.Fatal(err)
		}
		expectNamespace(t, deep, ns(cam))
	}
	// a wider prefix replaces the narrower request, which the origin would otherwise refuse as overlapping
	_, wide := listen(t, h.client(), ns("live"))
	got := map[string]bool{}
	for range 2 {
		n, ok := recv(t, wide.messages, "a handed-over NAMESPACE").(*message.Namespace)
		if !ok {
			t.Fatalf("got %+v", n)
		}
		got[string(n.Suffix[1])] = true
	}
	if !got["cam1"] || !got["cam2"] {
		t.Errorf("the narrow request's reports carry over: %v", got)
	}
	up := recv(t, h.o.namespaces, "the wide SUBSCRIBE_NAMESPACE")
	if m := up.Message().(*message.SubscribeNamespace); !m.Prefix.Equal(ns("live")) {
		t.Fatalf("asked for %v", m.Prefix)
	}
	eventually(t, "the narrow request ends", func() bool { return narrow.Err() != nil })
	if err := up.Send(&message.Namespace{Suffix: ns("studio", "cam1")}); err != nil {
		t.Fatal(err)
	}
	// after the handover, what the wide request did not report again is withdrawn, and nothing else flaps
	if d, ok := recv(t, deep.messages, "NAMESPACE_DONE after the handover").(*message.NamespaceDone); !ok ||
		!d.Suffix.Equal(ns("cam2")) {
		t.Fatalf("got %+v", d)
	}
	expectNamespaceDone(t, wide, ns("studio", "cam2"))
	expectQuiet(t, deep)
	expectQuiet(t, wide)
}

func TestOriginNamespacesRenewed(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	h.o.nsExpires = 40
	listen(t, h.client(), ns("live"))
	recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	// the relay extends the subscription before each EXPIRES the origin gives elapses (§9.20.16)
	eventually(t, "two renewals", func() bool { return h.o.nsRenewals.Load() >= 2 })
}

func TestOriginNamespacesBesidePublishers(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	h.relay.SetAccess(relay.Access{Publish: true})
	_, watched := listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	ann, err := h.client().Open(context.Background(), &message.PublishNamespace{Prefix: ns("live", "a")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	expectNamespace(t, watched, ns("a"))
	// a namespace both the origin and a publisher cover is reported once, and withdrawn when neither does
	if err = up.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	if err = up.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	if err = up.Send(&message.NamespaceDone{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, watched)
	ann.Cancel(message.ResetCancelled)
	expectNamespaceDone(t, watched, ns("a"))
}

func TestOriginNamespacesResubscribe(t *testing.T) {
	h := originNamespaces(t, relay.Options{ReconnectBackoff: 10 * time.Millisecond})
	_, watched := listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	if err := up.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, watched, ns("a"))
	// a lost origin subscription withdraws its namespaces and is opened again
	up.Cancel(message.ResetInternalError)
	expectNamespaceDone(t, watched, ns("a"))
	again := recv(t, h.o.namespaces, "the relay's second SUBSCRIBE_NAMESPACE")
	if err := again.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, watched, ns("a"))

	// so is one lost with its session
	h.up.kill()
	expectNamespaceDone(t, watched, ns("a"))
	recv(t, h.o.namespaces, "the SUBSCRIBE_NAMESPACE on a new session")
}

func TestOriginRefusesNamespaces(t *testing.T) {
	h := newHarness(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond}, nil)
	h.relay.SetAccess(relay.Access{Publish: true})
	_, watched := listen(t, h.client(), ns("live"))
	eventually(t, "the origin's refusal", func() bool { return h.o.nsAsks.Load() == 1 })
	// a refusal is final; the listener still hears publishers
	ann, err := h.client().Open(context.Background(), &message.PublishNamespace{Prefix: ns("live", "a")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	expectNamespace(t, watched, ns("a"))
	if n := h.o.nsAsks.Load(); n != 1 {
		t.Errorf("a refused SUBSCRIBE_NAMESPACE was retried: %d asks", n)
	}
	if rel := h.up.releases(); len(rel) != 1 || rel[0] != nil {
		t.Errorf("a refusal blames no origin: %v", rel)
	}
}

func TestOriginNamespacesLinger(t *testing.T) {
	h := originNamespaces(t, relay.Options{Linger: time.Hour})
	req, _ := listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	if err := up.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	// a listener returning within the linger finds the origin subscription warm
	req.Cancel(message.ResetCancelled)
	_, back := listen(t, h.client(), ns("live"))
	expectNamespace(t, back, ns("a"))
	if n := h.o.nsAsks.Load(); n != 1 {
		t.Errorf("the origin was asked %d times", n)
	}

	short := originNamespaces(t, relay.Options{Linger: 10 * time.Millisecond})
	req, _ = listen(t, short.client(), ns("live"))
	up = recv(t, short.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	req.Cancel(message.ResetCancelled)
	eventually(t, "the origin subscription ends after the linger", func() bool { return up.Err() != nil })
	listen(t, short.client(), ns("live"))
	recv(t, short.o.namespaces, "a fresh SUBSCRIBE_NAMESPACE")
}

func TestOriginNamespacesDraft17(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	h.up.draft = 17
	listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	if m := up.Message().(*message.SubscribeNamespace); m.Options != message.SubscribeOptionNamespace {
		t.Errorf("draft -17 asked with options %d", m.Options)
	}
}

func TestOriginNamespacesEndWithRelay(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	h.relay.Close()
	eventually(t, "the origin subscription ends with the relay", func() bool { return up.Err() != nil })
}

func TestOriginNamespacesWaitForOrigin(t *testing.T) {
	h := originNamespaces(t, relay.Options{ReconnectBackoff: 5 * time.Millisecond})
	h.up.down.Store(true)
	listen(t, h.client(), ns("live"))
	// an unreachable origin is tried again with a backoff, and the listener is answered once it returns
	time.Sleep(20 * time.Millisecond)
	h.up.down.Store(false)
	recv(t, h.o.namespaces, "the SUBSCRIBE_NAMESPACE once the origin is back")
}

func TestOriginPublishedNamespaces(t *testing.T) {
	h := originNamespaces(t, relay.Options{})
	_, here := listen(t, h.client(), ns("live"))
	up := recv(t, h.o.namespaces, "the relay's SUBSCRIBE_NAMESPACE")
	// another scope shares the one origin subscription, since what the origin reports is backend-wide
	h.scope = relay.Scope{Authority: "other.test", Path: "/"}
	_, there := listen(t, h.client(), ns("live"))
	if n := h.o.nsAsks.Load(); n != 1 {
		t.Errorf("the origin was asked %d times", n)
	}
	// a namespace the origin reports reaches every scope, and one it also publishes on an upstream session is
	// reported once
	if err := up.Send(&message.Namespace{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	expectNamespace(t, here, ns("a"))
	expectNamespace(t, there, ns("a"))
	withdraw := h.relay.OriginNamespace(ns("live", "a"))
	withdraw()
	withdraw()
	expectQuiet(t, here)
	expectQuiet(t, there)
	// a namespace only published on the upstream session is reported, and withdrawn with it
	gone := h.relay.OriginNamespace(ns("live", "b"))
	expectNamespace(t, here, ns("b"))
	expectNamespace(t, there, ns("b"))
	gone()
	expectNamespaceDone(t, here, ns("b"))
	expectNamespaceDone(t, there, ns("b"))
	if err := up.Send(&message.NamespaceDone{Suffix: ns("a")}); err != nil {
		t.Fatal(err)
	}
	expectNamespaceDone(t, here, ns("a"))
	expectNamespaceDone(t, there, ns("a"))
}
