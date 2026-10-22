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
	"errors"
	"testing"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

func TestNewClientRefusals(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		tls    *to.Options
	}{
		{"not a moq origin", "http://relay.example/", nil},
		{"no host", "moqt:///live", nil},
		{"unreadable CA", "moqt://relay.example/", &to.Options{CertificateAuthorityPaths: []string{"/nonexistent/ca.pem"}}},
	}
	for _, tc := range tests {
		o := bo.New()
		o.Name, o.Provider, o.ListenerNames, o.OriginURL, o.TLS = tc.name, "moq", []string{"moq1"}, tc.origin, tc.tls
		o.MoQ = mo.New()
		if _, err := NewClient(tc.name, o, nil, nil, nil, nil); err == nil {
			t.Errorf("%s: the backend is refused", tc.name)
		}
	}
}

func TestHelperEdges(t *testing.T) {
	if memberClient(nil) != nil || memberClient(&lb.Member{Value: "not a target"}) != nil {
		t.Error("a member that is no pool target has no client")
	}
	if got := offeredProtocols([]string{`"moqt-17", (`}); got == nil || len(got) != 0 {
		t.Errorf("an unparsable list offers nothing usable: %#v", got)
	}
	if got := offeredProtocols([]string{""}); got != nil {
		t.Errorf("an empty list offers nothing at all: %#v", got)
	}
	if sessionEnd(nil) != "" || sessionEnd(errors.New("gone")) != "gone" {
		t.Error("a session's end reason")
	}
}
