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

package static

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestWithResponseHeaders(t *testing.T) {
	s := newTestServer(t, testOptions(newTestSite(t)))
	if h := withResponseHeaders(nil, s); h != http.Handler(s) {
		t.Error("expected no wrapper when no headers are configured")
	}
	h := withResponseHeaders(map[string]string{"x-frame-options": "DENY"}, s)
	for _, target := range []string{"/", "/nope"} {
		resp := get(t, h, http.MethodGet, target)
		if resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: expected the configured response header", target)
		}
	}
}

func TestWithDirectoryListing(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "empty", "sub", "b.txt"), "b")
	writeFile(t, filepath.Join(root, "empty", ".secret"), "secret")
	writeFile(t, filepath.Join(root, "empty", "<b>&.txt"), "escaped")
	if err := os.Symlink(filepath.Join(filepath.Dir(root), "outside"), filepath.Join(root, "empty", "escape")); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, testOptions(root))
	s.start()
	h := withDirectoryListing(s, s)

	resp := get(t, h, http.MethodGet, "/empty/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected a listing, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get(headers.NameContentType); ct != contentTypeHTML {
		t.Errorf("expected an html listing, got %q", ct)
	}
	if resp.Header.Get(headers.NameCacheControl) != headers.ValueNoCache {
		t.Error("expected a listing not to be reused without revalidation")
	}
	page := body(t, resp)
	for _, want := range []string{
		"Index of /empty/", `<a href="../">`, `<a href="./sub/">sub/</a>`,
		`<a href="./a.txt">a.txt</a>`, "&lt;b&gt;&amp;.txt", `href="./%3Cb%3E&amp;.txt"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("expected the listing to contain %q:\n%s", want, page)
		}
	}
	for _, unwanted := range []string{".secret", "escape", "<b>"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("expected the listing not to contain %q", unwanted)
		}
	}
	if strings.Index(page, "sub/") > strings.Index(page, "a.txt") {
		t.Error("expected directories to be listed before files")
	}

	resp = get(t, h, http.MethodHead, "/empty/")
	if resp.StatusCode != http.StatusOK || body(t, resp) != "" ||
		resp.Header.Get(headers.NameContentLength) == "" {
		t.Error("expected a HEAD listing to carry headers and no body")
	}

	// the site root has a default file, so it is served rather than listed
	writeFile(t, filepath.Join(root, "bare", "only.txt"), "only")
	for range 2 {
		if resp = get(t, h, http.MethodGet, "/"); body(t, resp) != testHome {
			t.Error("expected the default file in place of a listing")
		}
	}
	if resp = get(t, h, http.MethodGet, "/bare/"); strings.Contains(body(t, resp), `href="../"`) == false {
		t.Error("expected a parent link in a subdirectory listing")
	}

	passthrough := []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/empty", http.StatusMovedPermanently},
		{http.MethodGet, "/empty/a.txt", http.StatusOK},
		{http.MethodGet, "/empty/a.txt/", http.StatusNotFound},
		{http.MethodGet, "/nope/", http.StatusNotFound},
		{http.MethodGet, "/.git/", http.StatusNotFound},
		{http.MethodPost, "/empty/", http.StatusMethodNotAllowed},
	}
	for _, test := range passthrough {
		if resp = get(t, h, test.method, test.target); resp.StatusCode != test.status {
			t.Errorf("%s %s: expected %d got %d", test.method, test.target, test.status, resp.StatusCode)
		}
	}
}

func TestDirectoryListingOfRoot(t *testing.T) {
	root := newTestSite(t)
	if err := os.Remove(filepath.Join(root, "index.html")); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, testOptions(root))
	page := body(t, get(t, withDirectoryListing(s, s), http.MethodGet, "/"))
	if !strings.Contains(page, `<a href="./docs/">docs/</a>`) || strings.Contains(page, `href="../"`) {
		t.Errorf("expected a root listing with no parent link:\n%s", page)
	}
	if strings.Contains(page, ".env") || strings.Contains(page, ".git") {
		t.Error("expected dotfiles to be left out of the listing")
	}
}
