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

package options

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestBackendDefaultsAndOverlay(t *testing.T) {
	var o Options
	in := "upstream:\n  prewarm: true\n  drafts: [20]\nlive_edge:\n  groups: 5\nspill: false\n"
	if err := yaml.Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	if !o.Upstream.Prewarm || !slices.Equal(o.Upstream.Drafts, []int{20}) || o.LiveEdge.Groups != 5 || o.Spill {
		t.Errorf("overlay %+v %+v", o.Upstream, o.LiveEdge)
	}
	if o.Upstream.ReconnectBackoff != timeconv.Duration(DefaultReconnectBackoff) ||
		o.LiveEdge.MaxSizeBytes != DefaultLiveEdgeSizeBytes || o.FillTimeout != timeconv.Duration(DefaultFillTimeout) {
		t.Error("unset fields keep their defaults")
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	c := o.Clone()
	c.Upstream.Drafts[0] = 21
	c.LiveEdge.Groups = 9
	if o.Upstream.Drafts[0] != 20 || o.LiveEdge.Groups != 5 {
		t.Error("a clone is independent")
	}
	if (*Options)(nil).Clone() != nil || (*Options)(nil).Validate() != nil {
		t.Error("nil options")
	}
}

func TestBackendValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
		want   string
	}{
		{"unsupported draft", func(o *Options) { o.Upstream.Drafts = []int{12} }, "not supported"},
		{"no drafts", func(o *Options) { o.Upstream.Drafts = nil }, "at least one"},
		{"repeated draft", func(o *Options) { o.Upstream.Drafts = []int{21, 21} }, "twice"},
		{"backoff", func(o *Options) { o.Upstream.ReconnectBackoff = 0 }, "reconnect_backoff"},
		{"linger", func(o *Options) { o.Upstream.Linger = -1 }, "linger"},
		{"gap fetches", func(o *Options) { o.Upstream.MaxGapFetches = 0 }, "max_gap_fetches"},
		{"groups", func(o *Options) { o.LiveEdge.Groups = 0 }, "groups"},
		{"live edge size", func(o *Options) { o.LiveEdge.MaxSizeBytes = 0 }, "max_size_bytes"},
		{"fill timeout", func(o *Options) { o.FillTimeout = timeconv.Duration(-time.Second) }, "fill_timeout"},
		{"object size", func(o *Options) { o.MaxObjectSizeBytes = 0 }, "max_object_size_bytes"},
		{"missing blocks", func(o *Options) { o.Upstream = nil }, "required"},
	}
	for _, tc := range tests {
		o := New()
		tc.mutate(o)
		if err := o.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestListenerOptions(t *testing.T) {
	var o ListenerOptions
	if err := yaml.Unmarshal([]byte("max_sessions: 10\ndrafts: [22]\nimplementation: edge\n"), &o); err != nil {
		t.Fatal(err)
	}
	if o.MaxSessions != 10 || !slices.Equal(o.Drafts, []int{22}) || o.Implementation != "edge" ||
		o.HandshakeTimeout != timeconv.Duration(DefaultHandshakeTimeout) {
		t.Errorf("listener options %+v", o)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	c := o.Clone()
	if !c.Equal(&o) || !(*ListenerOptions)(nil).Equal(nil) || c.Equal(nil) {
		t.Error("a clone is equal")
	}
	c.Drafts[0] = 20
	if o.Drafts[0] != 22 || c.Equal(&o) || (*ListenerOptions)(nil).Clone() != nil ||
		(*ListenerOptions)(nil).Validate() != nil {
		t.Error("clone and nil")
	}
	for name, mutate := range map[string]func(*ListenerOptions){
		"timeouts": func(o *ListenerOptions) { o.IdleTimeout = 0 },
		"sessions": func(o *ListenerOptions) { o.MaxSessions = 0 },
		"ranges":   func(o *ListenerOptions) { o.MaxFilterRanges = -1 },
		"drafts":   func(o *ListenerOptions) { o.Drafts = []int{99} },
	} {
		l := NewListener()
		mutate(l)
		if l.Validate() == nil {
			t.Errorf("%s: invalid options accepted", name)
		}
	}
}

func TestWebTransportOptions(t *testing.T) {
	var o Options
	in := "webtransport:\n  default_draft: 22\n  allowed_origins: ['HTTPS://Player.Example/', '*']\n"
	if err := yaml.Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	w := o.WebTransport
	if w.DefaultDraft != 22 || !slices.Equal(w.Drafts, codec.ServerPreference) || w.MaxSessions != DefaultMaxSessions {
		t.Errorf("unset fields keep their defaults: %+v", w)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.AllowedOrigins, []string{"https://player.example", AnyOrigin}) {
		t.Errorf("origins are normalized: %v", w.AllowedOrigins)
	}
	c := o.Clone()
	c.WebTransport.Drafts[0], c.WebTransport.AllowedOrigins[0] = 20, "x"
	if w.Drafts[0] == 20 || w.AllowedOrigins[0] == "x" {
		t.Error("a clone is independent")
	}
	bare := New()
	bare.WebTransport = nil
	if err := bare.Validate(); err != nil || bare.WebTransport == nil {
		t.Error("a missing block takes its defaults")
	}
	tests := []struct {
		name   string
		mutate func(*WebTransportOptions)
		want   string
	}{
		{"bad draft", func(w *WebTransportOptions) { w.Drafts = []int{99} }, "not supported"},
		{"default not accepted", func(w *WebTransportOptions) { w.Drafts, w.DefaultDraft = []int{21}, 20 }, "default_draft"},
		{"no sessions", func(w *WebTransportOptions) { w.MaxSessions = 0 }, "max_sessions"},
		{"a path", func(w *WebTransportOptions) { w.AllowedOrigins = []string{"https://player.example/watch"} }, "origin"},
		{"no scheme", func(w *WebTransportOptions) { w.AllowedOrigins = []string{"player.example"} }, "origin"},
		{"a query", func(w *WebTransportOptions) { w.AllowedOrigins = []string{"https://player.example?x=1"} }, "origin"},
		{"credentials", func(w *WebTransportOptions) { w.AllowedOrigins = []string{"https://u:p@player.example"} }, "origin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := New()
			tc.mutate(o.WebTransport)
			if err := o.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
