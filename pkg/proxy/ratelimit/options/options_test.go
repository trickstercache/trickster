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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func valid() *Options {
	return &Options{Name: "per-client", Limit: 10, Keys: []string{"client_ip"}}
}

func TestValidateDefaults(t *testing.T) {
	o := valid()
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.Algorithm != AlgorithmSlidingWindow || o.Window != timeconv.Duration(time.Minute) ||
		o.IPv6Prefix != 64 || o.MissingKey != MissingExempt || o.Action != ActionReject ||
		o.PolicyHeaders != PolicyNone || o.MaxKeys != defaultMaxKeys || o.MaxKeysAction != OnFullAllow {
		t.Fatalf("defaults: %+v", o)
	}
	if o.Status != http.StatusTooManyRequests || string(o.Body) != defaultBody {
		t.Fatalf("response status %d body %q", o.Status, o.Body)
	}
	if got := o.Header.Get(headers.NameCacheControl); got != headers.ValueNoStore {
		t.Fatalf("cache-control %q", got)
	}
	if len(o.KeySources) != 1 || o.KeySources[0].Kind != flowkey.KeyClientIP {
		t.Fatalf("keys %+v", o.KeySources)
	}
	cloned := o.Clone()
	cloned.Keys[0] = "path"
	cloned.Header.Set(headers.NameContentType, "text/html")
	if o.Keys[0] != "client_ip" || o.Header.Get(headers.NameContentType) == "text/html" {
		t.Fatal("clone shares a slice or header")
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Options)
		want string
	}{
		{"algorithm", func(o *Options) { o.Algorithm = "token_bucket" }, "algorithm"},
		{"limit zero", func(o *Options) { o.Limit = 0 }, "limit"},
		{"limit huge", func(o *Options) { o.Limit = int64(^uint32(0)) + 1 }, "limit"},
		{"window short", func(o *Options) { o.Window = timeconv.Duration(time.Millisecond) }, "window"},
		{"window long", func(o *Options) { o.Window = timeconv.Duration(2 * time.Hour) }, "window"},
		{"ipv6", func(o *Options) { o.IPv6Prefix = 129 }, "ipv6_prefix"},
		{"missing key", func(o *Options) { o.MissingKey = "drop" }, "missing_key"},
		{"action", func(o *Options) { o.Action = "drop" }, "action"},
		{"policy", func(o *Options) { o.PolicyHeaders = "both" }, "policy_headers"},
		{"max keys", func(o *Options) { o.MaxKeys = -1 }, "max_keys"},
		{"max keys action", func(o *Options) { o.MaxKeysAction = "evict" }, "max_keys_action"},
		{"unit", func(o *Options) { o.Unit = "bytes" }, "unit"},
		{"too many keys", func(o *Options) { o.Keys = []string{"a", "b", "c", "d", "e", "f"} }, "keys has"},
		{"bad key", func(o *Options) { o.Keys = []string{"not-a-key"} }, "keys[0]"},
		{"status", func(o *Options) { o.Response = &Response{Status: 200} }, "status"},
		{"header break", func(o *Options) { o.Response = &Response{Headers: map[string]string{"X\r\nY": "z"}} }, "carriage return"},
		{"managed header", func(o *Options) { o.Response = &Response{Headers: map[string]string{"Retry-After": "1"}} }, "managed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := valid()
			o.Limit = 10
			tc.edit(o)
			err := o.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResponseKeepsNoStore(t *testing.T) {
	o := valid()
	o.Response = &Response{
		Status:  503,
		Body:    "slow\n",
		Headers: map[string]string{"Cache-Control": "public", "Content-Type": "application/json"},
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.Status != 503 || string(o.Body) != "slow\n" {
		t.Fatalf("status %d body %q", o.Status, o.Body)
	}
	if o.Header.Get(headers.NameCacheControl) != headers.ValueNoStore {
		t.Fatalf("cache-control %q", o.Header.Get(headers.NameCacheControl))
	}
	if o.Header.Get(headers.NameContentType) != "application/json" {
		t.Fatal("content type was not overlaid")
	}
}

func TestLookupRejectsNone(t *testing.T) {
	err := Lookup{reserved.ReferenceNone: valid()}.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid rate limiter name") {
		t.Fatal(err)
	}
	if err := (Lookup{"": valid()}).Validate(); err == nil {
		t.Fatal("empty name was accepted")
	}
	if err := (Lookup{"per-client": nil}).Validate(); err == nil {
		t.Fatal("nil definition was accepted")
	}
	if Lookup(nil).Clone() != nil || (*Options)(nil).Clone() != nil {
		t.Fatal("nil clone")
	}
}
