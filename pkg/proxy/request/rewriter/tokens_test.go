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
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	proxyurls "github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

func TestRewriteTokensAcrossInstructionTypes(t *testing.T) {
	instructions, err := ParseRewriteList(options.RewriteList{
		{"header", "set", "X-Tenant", "${value}:${unknown}"},
		{"param", "set", "tenant", "${value}"},
		{"path", "set", "/tenants/${value}"},
		{"hostname", "set", "${value}.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://old.example.com/original", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = WithTokens(req, map[string]string{"name": "Tenant", "value": "abc"})

	instructions.Execute(req)

	if got, want := req.Header.Get("X-Tenant"), "abc:${unknown}"; got != want {
		t.Errorf("X-Tenant = %q, want %q", got, want)
	}
	if got, want := req.URL.Query().Get("tenant"), "abc"; got != want {
		t.Errorf("tenant param = %q, want %q", got, want)
	}
	if got, want := req.URL.Path, "/tenants/abc"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := req.URL.Hostname(), "abc.example.com"; got != want {
		t.Errorf("hostname = %q, want %q", got, want)
	}
}

func TestRewriteTokensInKeyBasedInstructions(t *testing.T) {
	instructions, err := ParseRewriteList(options.RewriteList{
		{"header", "set", "X-Tenant", "before-${value}"},
		{"header", "replace", "X-${name}", "${value}", "after"},
		{"header", "append", "X-Tenant", "scope=${value}"},
		{"header", "set", "X-Delete", "scope=abc, keep=true"},
		{"header", "delete", "X-Delete", "scope=${value}"},
		{"param", "set", "Tenant", "before-${value}"},
		{"param", "replace", "${name}", "${value}", "after"},
		{"param", "append", "Tenant", "extra-${value}"},
		{"param", "set", "delete-Tenant", "${value}"},
		{"param", "delete", "delete-${name}", "${value}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{"name": "Tenant", "value": "abc"}
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = WithTokens(req, tokens)
	tokens["value"] = "changed"

	instructions.Execute(req)

	if got, want := req.Header.Get("X-Tenant"), "before-after, scope=abc"; got != want {
		t.Errorf("X-Tenant = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("X-Delete"), "keep=true"; got != want {
		t.Errorf("X-Delete = %q, want %q", got, want)
	}
	if got, want := req.URL.Query()["Tenant"], []string{"before-after", "extra-abc"}; !slices.Equal(got, want) {
		t.Errorf("Tenant params = %q, want %q", got, want)
	}
	if req.URL.Query().Has("delete-Tenant") {
		t.Errorf("delete param remains in %q", req.URL.RawQuery)
	}
}

func TestRewriteTokensInScalarAndPathInstructions(t *testing.T) {
	t.Run("path", func(t *testing.T) {
		instructions, err := ParseRewriteList(options.RewriteList{
			{"path", "set", "/tenants/${value}/old"},
			{"path", "set", "${value}", "1"},
			{"path", "replace", "${value}", "${name}", "1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodGet, "http://example.com/original", nil)
		if err != nil {
			t.Fatal(err)
		}
		req = WithTokens(req, map[string]string{"name": "Tenant", "value": "abc"})

		instructions.Execute(req)

		if got, want := req.URL.Path, "/tenants/Tenant/old"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})

	t.Run("scalars", func(t *testing.T) {
		instructions, err := ParseRewriteList(options.RewriteList{
			{"params", "set", "tenant=${value}&state=old"},
			{"params", "replace", "old", "${name}"},
			{"method", "set", "${method}"},
			{"host", "set", "${value}.example.com:${port}"},
			{"host", "replace", "${value}", "${name}"},
			{"hostname", "replace", "${name}", "${value}"},
			{"port", "replace", "${port}", "8080"},
			{"scheme", "set", "${scheme}"},
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodGet, "http://old.example.com/original", nil)
		if err != nil {
			t.Fatal(err)
		}
		req = WithTokens(req, map[string]string{
			"method": "POST",
			"name":   "Tenant",
			"port":   "9090",
			"scheme": "https",
			"value":  "abc",
		})

		instructions.Execute(req)

		if got, want := req.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := req.URL.String(),
			"https://abc.example.com:8080/original?tenant=abc&state=Tenant"; got != want {
			t.Errorf("URL = %q, want %q", got, want)
		}
	})
}

func TestRewriteTokensPassThroughChains(t *testing.T) {
	lookup, err := ProcessConfigs(options.Lookup{
		"parent": {
			Instructions: options.RewriteList{{"chain", "exec", "child"}},
		},
		"child": {
			Instructions: options.RewriteList{{"header", "set", "X-Chain", "${value}"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = WithTokens(req, map[string]string{"value": "abc"})

	lookup["parent"].Execute(req)

	if got, want := req.Header.Get("X-Chain"), "abc"; got != want {
		t.Errorf("X-Chain = %q, want %q", got, want)
	}
	if !lookup["parent"].HasTokens() {
		t.Error("parent chain did not propagate token use")
	}
}

func TestUnmatchedAuthorityReplacementDoesNotOverrideUpstream(t *testing.T) {
	instructions, err := ParseRewriteList(options.RewriteList{
		{"hostname", "replace", "missing.example.com", "other.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://frontend.example.com/path", nil)
	if err != nil {
		t.Fatal(err)
	}

	instructions.Execute(req)

	base, err := url.Parse("http://origin.example.com:9090")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := proxyurls.BuildUpstreamURL(req, base).Host,
		"origin.example.com:9090"; got != want {
		t.Errorf("upstream host = %q, want %q", got, want)
	}
}

func TestAuthorityRewritersSupportIPv6(t *testing.T) {
	instructions, err := ParseRewriteList(options.RewriteList{
		{"hostname", "set", "2001:db8::2"},
		{"port", "set", "8443"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://[2001:db8::1]:9090/path", nil)
	if err != nil {
		t.Fatal(err)
	}

	instructions.Execute(req)

	if got, want := req.URL.Host, "[2001:db8::2]:8443"; got != want {
		t.Errorf("request host = %q, want %q", got, want)
	}
	base, err := url.Parse("http://[2001:db8::3]:7070")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := proxyurls.BuildUpstreamURL(req, base).Host,
		"[2001:db8::2]:8443"; got != want {
		t.Errorf("upstream host = %q, want %q", got, want)
	}

	deletePort, err := ParseRewriteList(options.RewriteList{{"port", "delete"}})
	if err != nil {
		t.Fatal(err)
	}
	deletePort.Execute(req)
	if got, want := req.URL.Host, "[2001:db8::2]"; got != want {
		t.Errorf("request host after port delete = %q, want %q", got, want)
	}
}

func TestWithTokensNilRequest(t *testing.T) {
	if WithTokens(nil, map[string]string{"value": "abc"}) != nil {
		t.Fatal("expected nil request")
	}
	if WithoutTokens(nil) != nil {
		t.Fatal("expected nil request")
	}
}

func TestWithoutTokens(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = WithTokens(req, map[string]string{"value": "abc"})
	req = WithoutTokens(req)
	if got, want := expandTokens(req, "${value}"), "${value}"; got != want {
		t.Fatalf("expanded value = %q, want %q", got, want)
	}
}

func TestWithoutTokensReturnsUnchangedRequestWhenEmpty(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := WithoutTokens(req); got != req {
		t.Fatal("request without tokens was cloned")
	}
}

func TestExpandTokens(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = WithTokens(req, map[string]string{
		"empty":  "",
		"nested": "${value}",
		"value":  "abc",
	})
	tests := []struct {
		name, input, want string
	}{
		{"single", "prefix-${value}-suffix", "prefix-abc-suffix"},
		{"multiple", "${value}/${empty}/${value}", "abc//abc"},
		{"leading closing brace", "prefix}${value}", "prefix}abc"},
		{"unknown", "${unknown}", "${unknown}"},
		{"not recursive", "${nested}", "${value}"},
		{"unterminated", "${value", "${value"},
		{"plain", "value", "value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := expandTokens(req, test.input); got != test.want {
				t.Errorf("expandTokens(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
