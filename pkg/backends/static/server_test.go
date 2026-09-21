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
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	so "github.com/trickstercache/trickster/v2/pkg/backends/static/options"
	"github.com/trickstercache/trickster/v2/pkg/encoding/brotli"
	"github.com/trickstercache/trickster/v2/pkg/encoding/deflate"
	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	eh "github.com/trickstercache/trickster/v2/pkg/encoding/handler"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zstd"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	testHome = "<h1>home</h1>"
	testDocs = "<h1>docs</h1>"
	testCSS  = "body{}"

	testCacheControl = "no-cache"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replaceFile swaps a file in atomically, so no reader sees partial content
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(filepath.Dir(path)), "tmp-"+filepath.Base(path))
	writeFile(t, tmp, content)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func newTestSite(t *testing.T) string {
	t.Helper()
	// resolved, so paths reported by the watcher match the configured root
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "site")
	writeFile(t, filepath.Join(root, "index.html"), testHome)
	writeFile(t, filepath.Join(root, "app.css"), testCSS)
	writeFile(t, filepath.Join(root, "docs", "index.html"), testDocs)
	writeFile(t, filepath.Join(root, "docs", "guide.txt"), "guide")
	writeFile(t, filepath.Join(root, "empty", "a.txt"), "a")
	writeFile(t, filepath.Join(root, ".env"), "SECRET=1")
	writeFile(t, filepath.Join(root, ".git", "config"), "secret")
	writeFile(t, filepath.Join(root, "docs", ".hidden"), "secret")
	return root
}

func testOptions(root string) *so.Options {
	o := so.New()
	o.Root = root
	o.FileserverCache.RevalidationInterval = 0
	return o
}

// newTestServer returns a server that is never started unless the caller
// does so; one with a zero revalidation interval gets a test-sized one.
func newTestServer(t *testing.T, o *so.Options) *server {
	t.Helper()
	if o.FileserverCache != nil && o.FileserverCache.RevalidationInterval <= 0 {
		o.FileserverCache.RevalidationInterval = 10 * 1000 * 1000 * 60 * 60 // 10h
	}
	s, err := newServer(testName(t), o, sets.New(bo.DefaultCompressibleTypes()))
	if err != nil {
		t.Fatal(err)
	}
	testServers.Store(s, struct{}{})
	t.Cleanup(func() {
		s.stop()
		testServers.Delete(s)
		// left to a finalizer in service, where requests may still be using it; here nothing is,
		// and a run of many tests would otherwise end with thousands of them still open
		s.root.Load().root.Close()
	})
	return s
}

// testServers is the servers under test. Their cache stores are made away from the request
// that caused them, so requests made through get wait for them before the test looks.
var testServers sync.Map

func awaitStores() {
	testServers.Range(func(s, _ any) bool {
		s.(*server).stores.Wait()
		return true
	})
}

func get(t *testing.T, h http.Handler, method, target string, hdrs ...string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(method, "http://example.com/", nil)
	// assigned rather than parsed, so paths a client would normalize arrive as written
	r.URL.Path, r.URL.RawQuery, _ = strings.Cut(target, "?")
	for i := 0; i+1 < len(hdrs); i += 2 {
		r.Header.Set(hdrs[i], hdrs[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	awaitStores()
	return w.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitFor(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return condition()
}

func TestNewServerErrors(t *testing.T) {
	if _, err := newServer("test", nil, nil); err != ErrMissingOptions {
		t.Errorf("expected ErrMissingOptions, got %v", err)
	}
	o := testOptions(filepath.Join(t.TempDir(), "missing"))
	if _, err := newServer("test", o, nil); err == nil {
		t.Error("expected an error for a missing root")
	}
	// a zero interval is rejected by the watcher
	o = testOptions(t.TempDir())
	if _, err := newServer("test", o, nil); err == nil {
		t.Error("expected an error for an invalid revalidation interval")
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		path, name string
		dir, ok    bool
	}{
		{"/", ".", true, true},
		{"", ".", true, true},
		{"/index.html", "index.html", false, true},
		{"docs/", "docs", true, true},
		{"/docs/guide.txt", "docs/guide.txt", false, true},
		{"/docs//guide.txt", "docs/guide.txt", false, true},
		{"/docs/../app.css", "app.css", false, true},
		{"/../../etc/passwd", "etc/passwd", false, true},
		{"/.env", "", false, false},
		{"/.git/config", "", false, false},
		{"/docs/.hidden", "", false, false},
		{"/docs/../.env", "", false, false},
		// the one name exempt from the refusal of dotfiles, and only at the top
		{"/.well-known/security.txt", ".well-known/security.txt", false, true},
		{"/.well-known", ".well-known", false, true},
		{"/.well-known/", ".well-known", true, true},
		{"/.well-known/acme-challenge/token", ".well-known/acme-challenge/token", false, true},
		{"/.well-known/.secret", "", false, false},
		{"/.well-known/x/.secret", "", false, false},
		{"/docs/.well-known/security.txt", "", false, false},
		{"/.well-knownx/security.txt", "", false, false},
		{"/.well-know", "", false, false},
		{"/docs\\..\\.env", "", false, false},
		{"/docs\x00.html", "", false, false},
		{"/...", "", false, false},
	}
	for _, test := range tests {
		name, dir, ok := resolve(test.path)
		if name != test.name || dir != test.dir || ok != test.ok {
			t.Errorf("resolve(%q) = (%q, %t, %t), expected (%q, %t, %t)",
				test.path, name, dir, ok, test.name, test.dir, test.ok)
		}
	}
}

func TestServeStatus(t *testing.T) {
	root := newTestSite(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	writeFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	// a relative link within the root is followed; an absolute one never is
	if err := os.Symlink("app.css", filepath.Join(root, "link.css")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "app.css"), filepath.Join(root, "abs.css")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "dirdefault", "index.html", "x.txt"), "x")

	for _, started := range []bool{false, true} {
		s := newTestServer(t, testOptions(root))
		if started {
			s.start()
		}
		tests := []struct {
			method, target string
			status         int
			body, location string
		}{
			{http.MethodGet, "/", http.StatusOK, testHome, ""},
			{http.MethodGet, "/index.html", http.StatusOK, testHome, ""},
			{http.MethodGet, "/docs/", http.StatusOK, testDocs, ""},
			{http.MethodGet, "/docs/guide.txt", http.StatusOK, "guide", ""},
			{http.MethodGet, "/link.css", http.StatusOK, testCSS, ""},
			{http.MethodHead, "/app.css", http.StatusOK, "", ""},
			{http.MethodGet, "/docs", http.StatusMovedPermanently, "", "docs/"},
			{http.MethodGet, "/docs?a=b", http.StatusMovedPermanently, "", "docs/?a=b"},
			// a directory without the default file is absent, not forbidden
			{http.MethodGet, "/empty/", http.StatusNotFound, "", ""},
			{http.MethodGet, "/dirdefault/", http.StatusNotFound, "", ""},
			{http.MethodGet, "/nope", http.StatusNotFound, "", ""},
			{http.MethodGet, "/nope/", http.StatusNotFound, "", ""},
			{http.MethodGet, "/app.css/", http.StatusNotFound, "", ""},
			{http.MethodGet, "/.env", http.StatusNotFound, "", ""},
			{http.MethodGet, "/.git/config", http.StatusNotFound, "", ""},
			{http.MethodGet, "/docs/.hidden", http.StatusNotFound, "", ""},
			{http.MethodGet, "/docs/../.env", http.StatusNotFound, "", ""},
			{http.MethodGet, "/../outside.txt", http.StatusNotFound, "", ""},
			{http.MethodGet, "/escape.txt", http.StatusNotFound, "", ""},
			{http.MethodGet, "/abs.css", http.StatusNotFound, "", ""},
			{http.MethodPost, "/", http.StatusMethodNotAllowed, "", ""},
			{http.MethodDelete, "/app.css", http.StatusMethodNotAllowed, "", ""},
			{http.MethodOptions, "/", http.StatusNoContent, "", ""},
		}
		// twice, so a started server answers from disk and then from memory
		for range 2 {
			for _, test := range tests {
				resp := get(t, s, test.method, test.target)
				if resp.StatusCode != test.status {
					t.Errorf("%s %s (started=%t): expected %d got %d",
						test.method, test.target, started, test.status, resp.StatusCode)
					continue
				}
				if b := body(t, resp); test.status == http.StatusOK && b != test.body {
					t.Errorf("%s %s: expected body %q got %q", test.method, test.target, test.body, b)
				}
				if loc := resp.Header.Get(headers.NameLocation); loc != test.location {
					t.Errorf("%s %s: expected location %q got %q", test.method, test.target, test.location, loc)
				}
				if test.status == http.StatusMethodNotAllowed || test.status == http.StatusNoContent {
					if resp.Header.Get(headers.NameAllow) != allowedMethods {
						t.Errorf("%s %s: expected an Allow header", test.method, test.target)
					}
				}
			}
		}
		if started && s.cache.count() == 0 {
			t.Error("expected a started server to hold files in memory")
		}
		if !started && s.cache.count() != 0 {
			t.Error("expected a server that was never started to hold nothing")
		}
	}
}

func TestServeHeaders(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "page.custom"), "custom")
	writeFile(t, filepath.Join(root, "README"), "<html><body>sniffed</body></html>")
	writeFile(t, filepath.Join(root, "blank"), "")
	writeFile(t, filepath.Join(root, "font.woff2"), "font")
	writeFile(t, filepath.Join(root, "UPPER.CSS"), testCSS)
	o := testOptions(root)
	o.CacheControl = testCacheControl
	o.CacheControlByExtension = map[string]string{"CSS": "public, max-age=60"}
	o.MIMETypes = map[string]string{"custom": "text/x-custom"}
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, o)
	s.start()

	tests := []struct {
		target, contentType, cacheControl string
	}{
		{"/", "text/html; charset=utf-8", testCacheControl},
		{"/app.css", "text/css; charset=utf-8", "public, max-age=60"},
		{"/UPPER.CSS", "text/css; charset=utf-8", "public, max-age=60"},
		{"/font.woff2", "font/woff2", testCacheControl},
		{"/page.custom", "text/x-custom", testCacheControl},
		{"/README", "text/html; charset=utf-8", testCacheControl},
		{"/blank", contentTypeOctetStream, testCacheControl},
	}
	for range 2 {
		for _, test := range tests {
			resp := get(t, s, http.MethodGet, test.target)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: expected 200 got %d", test.target, resp.StatusCode)
			}
			if ct := resp.Header.Get(headers.NameContentType); ct != test.contentType {
				t.Errorf("%s: expected content type %q got %q", test.target, test.contentType, ct)
			}
			if cc := resp.Header.Get(headers.NameCacheControl); cc != test.cacheControl {
				t.Errorf("%s: expected cache control %q got %q", test.target, test.cacheControl, cc)
			}
			etag := resp.Header.Get(headers.NameETag)
			if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
				t.Errorf("%s: expected a strong etag, got %q", test.target, etag)
			}
			if resp.Header.Get(headers.NameLastModified) == "" {
				t.Errorf("%s: expected a Last-Modified header", test.target)
			}
		}
	}

	// by default the header is left out, and clients judge freshness for themselves
	s = newTestServer(t, testOptions(root))
	for _, target := range []string{"/", "/app.css"} {
		resp := get(t, s, http.MethodGet, target)
		if _, ok := resp.Header[headers.NameCacheControl]; ok {
			t.Errorf("%s: expected no Cache-Control header by default", target)
		}
	}
}

func TestServeConditionalAndRanges(t *testing.T) {
	root := newTestSite(t)
	for _, started := range []bool{false, true} {
		s := newTestServer(t, testOptions(root))
		if started {
			s.start()
		}
		resp := get(t, s, http.MethodGet, "/")
		etag := resp.Header.Get(headers.NameETag)
		modified := resp.Header.Get(headers.NameLastModified)

		resp = get(t, s, http.MethodGet, "/", "If-None-Match", etag)
		if resp.StatusCode != http.StatusNotModified || body(t, resp) != "" {
			t.Errorf("expected an empty 304 for a matching etag, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "If-None-Match", `"other"`)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for a different etag, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "If-Modified-Since", modified)
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("expected 304 for an unmodified file, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "Range", "bytes=1-2")
		if resp.StatusCode != http.StatusPartialContent || body(t, resp) != testHome[1:3] {
			t.Errorf("expected a 206 with the requested bytes, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "Range", "bytes=0-0,2-3")
		if resp.StatusCode != http.StatusPartialContent ||
			!strings.HasPrefix(resp.Header.Get(headers.NameContentType), "multipart/byteranges") {
			t.Errorf("expected a multipart 206, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "Range", "bytes=500-")
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("expected 416 for an unsatisfiable range, got %d", resp.StatusCode)
		}
		resp = get(t, s, http.MethodGet, "/", "Range", "bytes=1-2", "If-Range", `"other"`)
		if resp.StatusCode != http.StatusOK || body(t, resp) != testHome {
			t.Errorf("expected the whole file for a failed If-Range, got %d", resp.StatusCode)
		}
	}
}

// compressibleCSS is large and repetitive enough that every encoding shrinks it
var compressibleCSS = strings.Repeat("body { color: red; margin: 0; padding: 0 }\n", 200)

func decode(t *testing.T, enc string, b string) string {
	t.Helper()
	var out []byte
	var err error
	switch enc {
	case "gzip":
		out, err = gzip.Decode([]byte(b))
	case "br":
		out, err = brotli.Decode([]byte(b))
	case "zstd":
		out, err = zstd.Decode([]byte(b))
	case "deflate":
		out, err = deflate.Decode([]byte(b))
	default:
		return b
	}
	if err != nil {
		t.Fatalf("unable to decode %s body: %v", enc, err)
	}
	return string(out)
}

func responses(s *server, status cacheStatus, enc providers.Provider) float64 {
	return testutil.ToFloat64(s.responses.Load()[status][enc])
}

func TestServeRenditions(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	writeFile(t, filepath.Join(root, "image.png"), strings.Repeat("png", 500))
	s := newTestServer(t, testOptions(root))
	s.start()
	// behind the response path's own encoder, as it is when routed, to prove that
	// a rendition is passed through it once rather than encoded again
	h := eh.HandleCompression(s, s.compressible)
	strong := get(t, h, http.MethodHead, "/big.css").Header.Get(headers.NameETag)

	// nothing is held: the file is read, encoded as Trickster prefers among what
	// the client accepts, and both are held under their own keys
	resp := get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip, deflate, br, zstd")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "zstd" {
		t.Fatalf("expected the preferred encoding for a new rendition, got %q", enc)
	}
	if got := decode(t, "zstd", body(t, resp)); got != compressibleCSS {
		t.Error("expected the rendition to decode to the file, having been encoded once")
	}
	if resp.Header.Get(headers.NameETag) != "W/"+strong || resp.Header.Get(headers.NameVary) != headers.NameAcceptEncoding {
		t.Errorf("expected the weak validator and Vary on a rendition, got %v", resp.Header)
	}
	if s.cache.held("big.css") == nil || s.cache.get("big.css", providers.Zstandard) == nil || s.cache.count() != 2 {
		t.Fatalf("expected the file and its rendition to be held, got %d entries", s.cache.count())
	}
	if responses(s, statusMiss, providers.Zstandard) != 1 {
		t.Error("expected a miss to be counted for the rendition that was served")
	}

	// the file is held but not as the client accepts it: a rendition is made from memory
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" ||
		decode(t, enc, body(t, resp)) != compressibleCSS {
		t.Errorf("expected a gzip rendition, got %q", enc)
	}
	if s.cache.get("big.css", providers.GZip) == nil || responses(s, statusPartialHit, providers.GZip) != 1 {
		t.Error("expected the new rendition to be held, and counted as a partial hit")
	}

	// of the renditions that are held (zstd and gzip), the one the client weights highest
	// is served; among equal weights, or none, Trickster's own preference decides
	for accept, want := range map[string]string{
		"gzip, deflate, br, zstd":    "zstd",
		"gzip;q=1.0, zstd;q=0.9":     "gzip",
		"br, zstd;q=0.5, gzip;q=0.4": "zstd",
		"zstd;q=0, GZIP":             "gzip",
		"identity, x-unknown":        "",
	} {
		before := s.cache.count()
		resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, accept)
		if enc := resp.Header.Get(headers.NameContentEncoding); enc != want ||
			decode(t, enc, body(t, resp)) != compressibleCSS {
			t.Errorf("Accept-Encoding %q: expected %q got %q", accept, want, enc)
		}
		if s.cache.count() != before {
			t.Errorf("Accept-Encoding %q: expected a held rendition to be reused", accept)
		}
	}
	if responses(s, statusHit, providers.GZip) != 2 || responses(s, statusHit, providers.Zstandard) != 2 ||
		responses(s, statusHit, providers.Identity) != 1 {
		t.Error("expected hits to be counted by the rendition served")
	}
	// held in none of what the client accepts, a rendition is made in what it weights highest
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "deflate;q=0.2, br;q=0.7")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "br" ||
		decode(t, enc, body(t, resp)) != compressibleCSS || s.cache.get("big.css", providers.Brotli) == nil {
		t.Errorf("expected a new rendition in the highest weighted encoding, got %q", enc)
	}

	// a HEAD is answered from a held rendition, and a matching validator with a 304
	resp = get(t, h, http.MethodHead, "/big.css", headers.NameAcceptEncoding, "gzip")
	if resp.Header.Get(headers.NameContentEncoding) != "gzip" || body(t, resp) != "" {
		t.Error("expected a HEAD to describe the held rendition")
	}
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip", "If-None-Match", strong)
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("expected a 304 for a rendition's validator, got %d", resp.StatusCode)
	}
	// byte ranges address the stored file, so a partial response is never a rendition
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip", "Range", "bytes=0-3")
	if resp.StatusCode != http.StatusPartialContent || body(t, resp) != compressibleCSS[:4] ||
		resp.Header.Get(headers.NameContentEncoding) != "" || resp.Header.Get(headers.NameETag) != strong {
		t.Errorf("expected an unencoded 206 with the strong validator, got %d %v", resp.StatusCode, resp.Header)
	}

	// a change to the file takes every rendition along with it
	replaceFile(t, filepath.Join(root, "big.css"), compressibleCSS+"/* v2 */")
	if !waitFor(5*time.Second, func() bool {
		resp := get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
		return decode(t, resp.Header.Get(headers.NameContentEncoding), body(t, resp)) == compressibleCSS+"/* v2 */"
	}) {
		t.Error("expected the renditions of a changed file to be dropped")
	}

	// a file of a type that isn't compressible has no renditions
	resp = get(t, h, http.MethodGet, "/image.png", headers.NameAcceptEncoding, "gzip")
	if resp.Header.Get(headers.NameContentEncoding) != "" || resp.Header.Get(headers.NameVary) != "" ||
		strings.HasPrefix(resp.Header.Get(headers.NameETag), "W/") || s.cache.get("image.png", providers.GZip) != nil {
		t.Error("expected a non-compressible file to be served as stored")
	}
}

func TestRenditionsAreNotMadeForResponsesWithoutBodies(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	h := eh.HandleCompression(s, s.compressible)
	etag := get(t, h, http.MethodHead, "/big.css", headers.NameAcceptEncoding, "gzip").Header.Get(headers.NameETag)
	resp := get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip", "If-None-Match", etag)
	if resp.StatusCode != http.StatusNotModified || s.cache.count() != 0 {
		t.Errorf("expected a HEAD and a 304 to read and hold nothing, got %d with %d held",
			resp.StatusCode, s.cache.count())
	}
	// held as stored, a conditional request still doesn't have a rendition made for it
	get(t, h, http.MethodGet, "/big.css")
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip", "If-None-Match", etag)
	if resp.StatusCode != http.StatusNotModified || s.cache.count() != 1 {
		t.Errorf("expected no rendition for a 304, got %d with %d held", resp.StatusCode, s.cache.count())
	}
	// one that turns out to need the body is encoded by the response path instead
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip", "If-None-Match", `"other"`)
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" ||
		decode(t, enc, body(t, resp)) != compressibleCSS || s.cache.count() != 1 {
		t.Errorf("expected the response path to encode it, got %q with %d held", enc, s.cache.count())
	}
}

func TestUnencodableFilesAreServedAsStored(t *testing.T) {
	s := newTestServer(t, testOptions(newTestSite(t)))
	s.start()
	h := eh.HandleCompression(s, s.compressible)
	// too small for encoding to shrink, which is found out once and remembered
	for range 3 {
		resp := get(t, h, http.MethodGet, "/app.css", headers.NameAcceptEncoding, "gzip")
		if resp.Header.Get(headers.NameContentEncoding) != "" || body(t, resp) != testCSS {
			t.Fatalf("expected the file as stored, got encoding %q", resp.Header.Get(headers.NameContentEncoding))
		}
		if strings.HasPrefix(resp.Header.Get(headers.NameETag), "W/") {
			t.Error("expected the strong validator on a response that is not encoded")
		}
	}
	if e := s.cache.held("app.css"); e == nil || e.unencodable.Load() == 0 || s.cache.count() != 1 {
		t.Error("expected the file to be held, marked, and without renditions")
	}
	if s.cache.size.Load() != entryCost("app.css", int64(len(testCSS))) {
		t.Error("expected the abandoned rendition's reservation to be released")
	}
}

func TestAcceptedEncodings(t *testing.T) {
	list := func(r *http.Request) []providers.Provider {
		a := acceptedEncodings(r)
		out := make([]providers.Provider, 0, a.Len())
		for i := range a.Len() {
			out = append(out, a.At(i))
		}
		return out
	}
	request := func(method string, hdrs ...string) *http.Request {
		r := httptest.NewRequest(method, "http://example.com/", nil)
		for i := 0; i+1 < len(hdrs); i += 2 {
			r.Header.Add(hdrs[i], hdrs[i+1])
		}
		return r
	}
	ae := headers.NameAcceptEncoding
	tests := []struct {
		name     string
		request  *http.Request
		expected []providers.Provider
	}{
		{"none", request(http.MethodGet), []providers.Provider{}},
		{"weighted", request(http.MethodGet, ae, "gzip, zstd;q=0.5, br;q=0.8"),
			[]providers.Provider{providers.GZip, providers.Brotli, providers.Zstandard}},
		{"unweighted", request(http.MethodGet, ae, "gzip, br"), []providers.Provider{providers.Brotli, providers.GZip}},
		{"head", request(http.MethodHead, ae, "gzip"), []providers.Provider{providers.GZip}},
		{"post", request(http.MethodPost, ae, "gzip"), []providers.Provider{}},
		{"range", request(http.MethodGet, ae, "gzip", headers.NameRange, "bytes=0-1"), []providers.Provider{}},
	}
	for _, test := range tests {
		if got := list(test.request); !slices.Equal(got, test.expected) {
			t.Errorf("%s: expected %v got %v", test.name, test.expected, got)
		}
	}
	// behind the response path, what it worked out is used, less what it has since ruled out
	r := request(http.MethodGet, ae, "deflate")
	ep := &profile.Profile{Accepted: providers.ParseAcceptEncoding("gzip;q=0.5, br")}
	ep.Supported = providers.GZip
	r = r.WithContext(profile.ToContext(r.Context(), ep))
	if got := list(r); !slices.Equal(got, []providers.Provider{providers.GZip}) {
		t.Errorf("expected the response path's reading of the request, got %v", got)
	}
	if encodingLabel(providers.Identity) != identityLabel || encodingLabel(providers.GZip) != "gzip" {
		t.Error("expected encodings to be labeled by name")
	}
}

func TestTeeBuffer(t *testing.T) {
	tee := &teeBuffer{limit: 5}
	for _, b := range []string{"ab", "cde"} {
		if n, err := tee.Write([]byte(b)); n != len(b) || err != nil {
			t.Fatalf("expected the write to be taken whole, got %d %v", n, err)
		}
	}
	if string(tee.buf) != "abcde" || tee.overflow {
		t.Errorf("expected writes within the limit to be collected, got %q", tee.buf)
	}
	// past the limit it stops collecting, without failing the response it shadows
	if n, err := tee.Write([]byte("f")); n != 1 || err != nil || !tee.overflow || tee.buf != nil {
		t.Errorf("expected an overflow to be absorbed, got %d %v", n, err)
	}
	if n, err := tee.Write([]byte("g")); n != 1 || err != nil || tee.buf != nil {
		t.Error("expected writes after an overflow to be absorbed too")
	}
}

func TestErrorPageWriterImplicitHeader(t *testing.T) {
	w := httptest.NewRecorder()
	e := &errorPageWriter{ResponseWriter: w}
	if _, err := e.Write([]byte("page")); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusNotFound || e.Unwrap() != w {
		t.Errorf("expected a write to imply a 404 on the wrapped writer, got %d", w.Code)
	}
	// a status other than the file's own 200 is not the error page's to change
	w = httptest.NewRecorder()
	(&errorPageWriter{ResponseWriter: w}).WriteHeader(http.StatusInternalServerError)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected another status to pass through, got %d", w.Code)
	}
}

type failingEncoder struct{}

func (failingEncoder) Write([]byte) (int, error) { return 0, errors.New("failed") }
func (failingEncoder) Close() error              { return nil }

func TestEncoderFailureIsNotRemembered(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	orig := newEncoder
	newEncoder = func(providers.Provider, io.Writer) io.WriteCloser { return failingEncoder{} }
	get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	newEncoder = orig
	if e := s.cache.held("big.css"); e == nil || e.unencodable.Load() != 0 || s.cache.files.Load() != 1 {
		t.Error("expected a failure to release its reservation without marking the file")
	}
	resp := get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" ||
		decode(t, enc, body(t, resp)) != compressibleCSS || s.cache.get("big.css", providers.GZip) == nil {
		t.Error("expected encoding to be tried again after a failure, and then held")
	}
}

func TestRenditionThatDoesNotShrinkIsNotHeld(t *testing.T) {
	root := newTestSite(t)
	// large enough to be tried, and random enough that encoding can only grow it
	noise := make([]byte, 4096)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "noise.txt"), string(noise))
	s := newTestServer(t, testOptions(root))
	s.start()
	resp := get(t, s, http.MethodGet, "/noise.txt", headers.NameAcceptEncoding, "gzip")
	if enc := resp.Header.Get(headers.NameContentEncoding); decode(t, enc, body(t, resp)) != string(noise) {
		t.Error("expected the response under way to be completed as it began")
	}
	if e := s.cache.held("noise.txt"); e == nil || e.unencodable.Load() == 0 || s.cache.count() != 1 ||
		s.cache.size.Load() != entryCost("noise.txt", int64(len(noise))) {
		t.Error("expected the file to be marked, and the rendition's reservation released")
	}
	resp = get(t, s, http.MethodGet, "/noise.txt", headers.NameAcceptEncoding, "gzip")
	if resp.Header.Get(headers.NameContentEncoding) != "" || body(t, resp) != string(noise) {
		t.Error("expected the file as stored from then on")
	}
}

func TestServingNeverWaitsOnTheCache(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	writeFile(t, filepath.Join(root, "held.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	get(t, s, http.MethodGet, "/held.css", headers.NameAcceptEncoding, "gzip")
	if s.cache.count() != 2 {
		t.Fatal("expected a file and its rendition to be held")
	}

	// with the cache's lock held against them, requests of every kind must still complete
	s.cache.mtx.Lock()
	served := make(chan string, 1)
	go func() {
		defer close(served)
		for _, test := range []struct{ target, accept string }{
			{"/held.css", "gzip"}, // a held rendition
			{"/held.css", ""},     // a held file
			{"/held.css", "br"},   // a held file, in a rendition that isn't
			{"/big.css", "gzip"},  // nothing held, and a rendition wanted
			{"/big.css", ""},      // nothing held
			{"/nope.css", ""},     // nothing there
		} {
			r := httptest.NewRequest(http.MethodGet, "http://example.com"+test.target, nil)
			if test.accept != "" {
				r.Header.Set(headers.NameAcceptEncoding, test.accept)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			want := compressibleCSS
			if test.target == "/nope.css" {
				want = http.StatusText(http.StatusNotFound) + "\n"
			}
			if got := decode(t, w.Header().Get(headers.NameContentEncoding), w.Body.String()); got != want {
				served <- test.target + " " + test.accept
				return
			}
		}
	}()
	select {
	case failed, bad := <-served:
		if bad {
			t.Errorf("%s: expected the complete response while the cache was locked", failed)
		}
	case <-time.After(10 * time.Second):
		t.Error("a request waited on the cache's lock")
	}
	s.cache.mtx.Unlock()
	awaitStores()
	// what couldn't be reserved then is simply loaded by a later request
	if s.cache.count() != 2 {
		t.Errorf("expected nothing to have been held while the cache was locked, got %d", s.cache.count())
	}
	get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	if s.cache.count() != 4 || s.cache.files.Load() != 4 {
		t.Errorf("expected the file and its rendition to be held once the lock was free, got %d", s.cache.count())
	}
}

func TestHeldRenditionIsNeverDecodedToStandInForTheFile(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	// the file as stored is evicted, leaving only its gzip rendition held
	s.cache.mtx.Lock()
	s.cache.remove(s.cache.held("big.css"))
	s.cache.mtx.Unlock()
	if s.cache.count() != 1 || s.cache.get("big.css", providers.GZip) == nil {
		t.Fatal("expected only the rendition to remain")
	}
	misses := responses(s, statusMiss, providers.Identity)
	// asked for as stored, the file comes from disk rather than from decoding the rendition
	resp := get(t, s, http.MethodGet, "/big.css")
	if resp.Header.Get(headers.NameContentEncoding) != "" || body(t, resp) != compressibleCSS ||
		responses(s, statusMiss, providers.Identity) != misses+1 {
		t.Error("expected the file to be read from disk")
	}
	// and asked for in an encoding that isn't held, both the file and that rendition are then held
	s.cache.mtx.Lock()
	s.cache.remove(s.cache.held("big.css"))
	s.cache.mtx.Unlock()
	resp = get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "br")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "br" ||
		decode(t, enc, body(t, resp)) != compressibleCSS || responses(s, statusMiss, providers.Brotli) != 1 {
		t.Errorf("expected a rendition made from the file on disk, got %q", enc)
	}
	if s.cache.held("big.css") == nil || s.cache.get("big.css", providers.Brotli) == nil || s.cache.count() != 3 {
		t.Errorf("expected the file and the new rendition to be held beside the old one, got %d", s.cache.count())
	}
}

func TestNewRenditionIsStreamed(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	o := testOptions(root)
	o.CacheControl = testCacheControl
	s := newTestServer(t, o)
	s.start()
	// sent as it is encoded, so without a length; held, it is sent with its own
	first := get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	second := get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	if first.Header.Get(headers.NameContentLength) != "" {
		t.Error("expected a new rendition to be streamed, with no length known ahead of it")
	}
	sent, held := body(t, first), body(t, second)
	if sent != held || second.Header.Get(headers.NameContentLength) != strconv.Itoa(len(held)) {
		t.Error("expected the rendition that is held to be the bytes that were streamed")
	}
	for _, name := range []string{headers.NameETag, headers.NameLastModified, headers.NameVary,
		headers.NameContentType, headers.NameCacheControl, headers.NameContentEncoding} {
		if first.Header.Get(name) == "" || first.Header.Get(name) != second.Header.Get(name) {
			t.Errorf("expected %s to be the same streamed and held, got %q and %q",
				name, first.Header.Get(name), second.Header.Get(name))
		}
	}
	// held at its own size, rather than at the size of the buffer it was collected in
	if v := s.cache.get("big.css", providers.GZip); v == nil || cap(v.body) != len(v.body) ||
		v.cost != entryCost(renditionKey("big.css", providers.GZip), int64(len(v.body))) {
		t.Error("expected the rendition to be held and accounted for at its own size")
	}
}

func TestRenditionDoesNotCostTheFileItsPlace(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	o := testOptions(root)
	o.FileserverCache.MaxFiles = 1
	s := newTestServer(t, o)
	s.start()
	h := eh.HandleCompression(s, s.compressible)
	// with room for one object, the file is what is worth holding: every rendition is made from it
	for range 4 {
		resp := get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
		if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" ||
			decode(t, enc, body(t, resp)) != compressibleCSS {
			t.Fatalf("expected an encoded response, got %q", enc)
		}
		if s.cache.held("big.css") == nil || s.cache.count() != 1 {
			t.Fatal("expected the file to stay held, rather than be evicted for its own rendition")
		}
	}
	// read from disk once, and encoded from memory after that
	if responses(s, statusMiss, providers.GZip) != 1 || responses(s, statusPartialHit, providers.GZip) != 3 {
		t.Errorf("expected one read from disk and three from memory, got %v and %v",
			responses(s, statusMiss, providers.GZip), responses(s, statusPartialHit, providers.GZip))
	}
	if testutil.ToFloat64(s.cache.metrics.evictions) != 0 {
		t.Error("expected nothing to have been evicted")
	}
}

func TestConcurrentLoadsStartOneStore(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.start()
	r, f, m := openForLoad(t, s, "app.css")
	// a read in progress, with a burst of loads waiting to share it and its reservation
	rsv := s.cache.reserve("app.css", providers.Identity, root, m.size, nil)
	if rsv == nil {
		t.Fatal("expected the file to be reserved")
	}
	shared := &entry{fileMeta: *m, key: "app.css", body: []byte(testCSS)}
	release, leading := make(chan struct{}), make(chan struct{})
	go s.loads.Do("app.css", func() (any, error) {
		close(leading)
		<-release
		return loaded{entry: shared, pending: rsv}, nil
	})
	<-leading
	const waiters = 64
	var wg sync.WaitGroup
	for range waiters {
		wg.Go(func() {
			e, pending := s.load(r, f, m, "app.css")
			s.storeEntry(e, pending)
		})
	}
	// let the waiters join the read before it completes
	time.Sleep(50 * time.Millisecond)
	before := s.started.Load()
	close(release)
	wg.Wait()
	s.stores.Wait()
	if started := s.started.Load() - before; started != 1 {
		t.Errorf("expected %d loads that shared a read to start one store between them, got %d", waiters, started)
	}
	if s.cache.held("app.css") != shared || s.cache.files.Load() != 1 {
		t.Error("expected the one store to have been made")
	}
}

func TestConcurrentColdRequestsStartFewStores(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.start()
	const requests = 200
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/app.css", nil)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Body.String() != testCSS {
				t.Errorf("unexpected response %d %q", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	s.stores.Wait()
	// a read that finishes before a later request arrives is followed by another, but each
	// is one store however many requests shared it: nothing like one per request
	if started := s.started.Load(); started < 1 || started > 8 {
		t.Errorf("expected a handful of stores for %d requests, got %d", requests, started)
	}
	if s.cache.count() != 1 {
		t.Errorf("expected the file to be held once, got %d", s.cache.count())
	}
}

// inflatingEncoder writes more than it is given, as an encoding does to a file it is no use to
type inflatingEncoder struct{ w io.Writer }

func (i inflatingEncoder) Write(b []byte) (int, error) {
	if _, err := i.w.Write(append([]byte("inflated:"), b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (inflatingEncoder) Close() error { return nil }

func TestUnhelpfulEncodingLeavesTheOthersToBeTried(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	h := eh.HandleCompression(s, s.compressible)
	orig := newEncoder
	defer func() { newEncoder = orig }()
	newEncoder = func(enc providers.Provider, w io.Writer) io.WriteCloser {
		if enc == providers.Deflate {
			return inflatingEncoder{w}
		}
		return orig(enc, w)
	}
	// the first client to reach the file prefers the one encoding that doesn't shrink it
	get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "deflate, br;q=0.5")
	e := s.cache.held("big.css")
	if e == nil || e.unhelpful() != providers.Deflate ||
		s.cache.get("big.css", providers.Deflate) != nil {
		t.Fatal("expected only the encoding that failed to be marked, and not to be held")
	}
	// which costs the next client nothing: its own preference is still tried, and held
	resp := get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "br")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "br" ||
		decode(t, enc, body(t, resp)) != compressibleCSS || s.cache.get("big.css", providers.Brotli) == nil {
		t.Errorf("expected another encoding to still be tried and held, got %q", enc)
	}
	// a client that prefers the unhelpful one is given the next that it accepts
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "deflate, zstd;q=0.5")
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "zstd" ||
		decode(t, enc, body(t, resp)) != compressibleCSS {
		t.Errorf("expected the next most preferred encoding, got %q", enc)
	}
	// and one that accepts nothing else is sent the file as stored, by the response path too
	for range 2 {
		resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "deflate")
		if resp.Header.Get(headers.NameContentEncoding) != "" || body(t, resp) != compressibleCSS {
			t.Error("expected the file as stored when only the unhelpful encoding is accepted")
		}
	}
	// a conditional request is left to the response path, which passes the unhelpful one over too
	resp = get(t, h, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "deflate, gzip;q=0.1",
		"If-None-Match", `"other"`)
	if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" {
		t.Errorf("expected the response path to pass over the unhelpful encoding, got %q", enc)
	}
	if e.unhelpful() != providers.Deflate {
		t.Error("expected nothing else to have been marked")
	}
}

func TestConcurrentRenditionsShareOneStore(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.css"), compressibleCSS)
	s := newTestServer(t, testOptions(root))
	s.start()
	// as routed, behind the response path: a request that arrives between the shared read and
	// its store takes the file from disk, and is encoded on its way out rather than as a rendition
	h := eh.HandleCompression(s, s.compressible)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/big.css", nil)
			r.Header.Set(headers.NameAcceptEncoding, "gzip")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if enc := w.Header().Get(headers.NameContentEncoding); enc != "gzip" {
				t.Errorf("expected every response to be encoded, got %q", enc)
				return
			}
			if got, err := gzip.Decode(w.Body.Bytes()); err != nil || string(got) != compressibleCSS {
				t.Errorf("expected a complete rendition whether or not it was the one held: %v", err)
			}
		})
	}
	wg.Wait()
	awaitStores()
	// a burst may leave the rendition to a later request, as a busy cache is never waited for
	get(t, s, http.MethodGet, "/big.css", headers.NameAcceptEncoding, "gzip")
	if s.cache.count() != 2 || s.cache.files.Load() != 2 {
		t.Errorf("expected the file and one rendition to be held, got %d", s.cache.count())
	}
}

func TestCodingGuardImplicitHeader(t *testing.T) {
	w := httptest.NewRecorder()
	g := &codingGuard{ResponseWriter: w}
	if _, err := g.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if !g.wroteHeader || w.Code != http.StatusOK || g.Unwrap() != w {
		t.Error("expected a write to imply a 200 header on the wrapped writer")
	}
}

func TestFileserverCacheAdmission(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "big.bin"), strings.Repeat("x", 64))
	writeFile(t, filepath.Join(root, "unknown"), strings.Repeat("y", 64))

	o := testOptions(root)
	o.FileserverCache.MaxFileSizeBytes = 32
	// room for index.html and its overhead, but not for a second file
	o.FileserverCache.MaxSizeBytes = entryCost("index.html", int64(len(testHome))) + 10
	s := newTestServer(t, o)
	s.start()
	for range 2 {
		for _, target := range []string{"/big.bin", "/unknown", "/", "/app.css"} {
			if resp := get(t, s, http.MethodGet, target); resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: expected 200 got %d", target, resp.StatusCode)
			}
		}
	}
	// the large files exceed the per-file limit, and the other two take turns in the
	// room there is for one: whichever was asked for last is the one that is held
	if s.cache.count() != 1 || s.cache.held("app.css") == nil || s.cache.held("index.html") != nil {
		t.Errorf("expected only app.css to be held, got %d entries", s.cache.count())
	}
	if size := s.cache.size.Load(); size != entryCost("app.css", int64(len(testCSS))) {
		t.Errorf("expected only app.css to be accounted for, got %d bytes", size)
	}
	if s.cache.files.Load() != 1 || s.watcher.Watched() != 1 {
		t.Errorf("expected 1 file and 1 watch, got %d and %d", s.cache.files.Load(), s.watcher.Watched())
	}
	if responses(s, statusDisk, providers.Identity) != 4 || responses(s, statusMiss, providers.Identity) != 4 {
		t.Error("expected files too large to hold to be counted as sent from disk, and the rest as misses")
	}

	o = testOptions(root)
	o.FileserverCache.Disabled = true
	s = newTestServer(t, o)
	s.start()
	if resp := get(t, s, http.MethodGet, "/"); resp.StatusCode != http.StatusOK || body(t, resp) != testHome {
		t.Error("expected a server with no fileserver cache to serve from disk")
	}
	if s.cache != nil || s.watcher != nil {
		t.Error("expected no cache or watcher when the fileserver cache is disabled")
	}
	s.stop()
}

func TestInvalidationByEvent(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.start()
	for _, target := range []string{"/", "/docs/", "/docs/guide.txt", "/app.css"} {
		get(t, s, http.MethodGet, target)
	}
	if s.cache.count() != 4 {
		t.Fatalf("expected 4 held files, got %d", s.cache.count())
	}
	served := func(target, want string, status int) func() bool {
		return func() bool {
			resp := get(t, s, http.MethodGet, target)
			return resp.StatusCode == status && (status != http.StatusOK || body(t, resp) == want)
		}
	}
	replaceFile(t, filepath.Join(root, "app.css"), "body{color:red}")
	if !waitFor(5*time.Second, served("/app.css", "body{color:red}", http.StatusOK)) {
		t.Error("expected the replaced file to be served")
	}
	writeFile(t, filepath.Join(root, "index.html"), "rewritten in place")
	if !waitFor(5*time.Second, served("/", "rewritten in place", http.StatusOK)) {
		t.Error("expected the rewritten file to be served")
	}
	if err := os.Remove(filepath.Join(root, "docs", "guide.txt")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, served("/docs/guide.txt", "", http.StatusNotFound)) {
		t.Error("expected a removed file to be absent")
	}
	// a renamed directory takes the held files beneath it along
	if err := os.Rename(filepath.Join(root, "docs"), filepath.Join(root, "manual")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, served("/docs/", "", http.StatusNotFound)) {
		t.Error("expected files under a renamed directory to be absent")
	}
	if !waitFor(5*time.Second, served("/manual/", testDocs, http.StatusOK)) {
		t.Error("expected files under the new directory name to be served")
	}
	// an event from outside the root can't be mapped to a key, so everything goes
	s.onFileEvent(filepath.Dir(root))
	if s.cache.count() != 0 {
		t.Error("expected an unmappable event to purge the cache")
	}

	s.stop()
	if s.cache.count() != 0 || s.cache.held("index.html") != nil {
		t.Error("expected a stopped server to hold nothing")
	}
	if s.cache.size.Load() != 0 || s.cache.files.Load() != 0 || s.watcher.Watched() != 0 {
		t.Error("expected a stopped server to have released its capacity and watches")
	}
	if !served("/", "rewritten in place", http.StatusOK)() {
		t.Error("expected a stopped server to keep serving from disk")
	}
}

func TestFileEventLeavesOtherLoadsAlone(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.cache.activate()
	get(t, s, http.MethodGet, "/index.html")
	// loads in progress, in the changed file's directory and in another one
	sibling := s.cache.reserve("app.css", providers.Identity, root, int64(len(testCSS)), nil)
	nested := s.cache.reserve("docs/guide.txt", providers.Identity, filepath.Join(root, "docs"), 5, nil)
	if sibling == nil || nested == nil {
		t.Fatal("expected both files to be reserved")
	}
	// an ordinary file changes, as one of many does during a deployment
	s.onFileEvent(filepath.Join(root, "index.html"))
	s.onFileEvent(filepath.Join(root, "favicon.ico"))
	if s.cache.held("index.html") != nil {
		t.Error("expected the changed file to be dropped")
	}
	if !sibling.commit(testEntry("app.css", testCSS)) || !nested.commit(testEntry("docs/guide.txt", "guide")) {
		t.Error("expected loads of other files to be unaffected by the change")
	}
	// the directory changing is what takes the files beneath it along
	pending := s.cache.reserve("docs/index.html", providers.Identity, filepath.Join(root, "docs"),
		int64(len(testDocs)), nil)
	s.onFileEvent(filepath.Join(root, "docs"))
	if s.cache.held("docs/guide.txt") != nil || pending.commit(testEntry("docs/index.html", testDocs)) {
		t.Error("expected a changed directory to take its held and loading files along")
	}
	if s.cache.held("app.css") == nil {
		t.Error("expected a file outside the changed directory to stay held")
	}
}

func TestRevalidate(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	// active without a running watcher, so only revalidate can find changes
	s.cache.activate()
	for _, target := range []string{"/", "/app.css", "/docs/guide.txt"} {
		get(t, s, http.MethodGet, target)
	}
	s.revalidate()
	if s.cache.count() != 3 {
		t.Fatalf("expected unchanged files to stay held, got %d", s.cache.count())
	}
	replaceFile(t, filepath.Join(root, "app.css"), "body{color:red}")
	if err := os.Remove(filepath.Join(root, "docs", "guide.txt")); err != nil {
		t.Fatal(err)
	}
	if body(t, get(t, s, http.MethodGet, "/app.css")) != testCSS {
		t.Fatal("expected the held file until revalidation")
	}
	s.revalidate()
	if s.cache.count() != 1 || s.cache.held("index.html") == nil {
		t.Errorf("expected only the unchanged file to stay held, got %d", s.cache.count())
	}
	if body(t, get(t, s, http.MethodGet, "/app.css")) != "body{color:red}" {
		t.Error("expected the replaced file after revalidation")
	}
}

func TestRevalidateReplacedRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "v1", "index.html"), "v1")
	writeFile(t, filepath.Join(base, "v2", "index.html"), "v2")
	current := filepath.Join(base, "current")
	if err := os.Symlink(filepath.Join(base, "v1"), current); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, testOptions(current))
	s.cache.activate()
	if body(t, get(t, s, http.MethodGet, "/")) != "v1" {
		t.Fatal("expected v1")
	}
	next := filepath.Join(base, "next")
	if err := os.Symlink(filepath.Join(base, "v2"), next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, current); err != nil {
		t.Fatal(err)
	}
	s.revalidate()
	if body(t, get(t, s, http.MethodGet, "/")) != "v2" {
		t.Error("expected the swapped-in root to be served after revalidation")
	}
	// a root that is briefly absent keeps the open one until it returns
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	s.revalidate()
	if body(t, get(t, s, http.MethodGet, "/")) != "v2" {
		t.Error("expected the open root to keep serving while the path is absent")
	}
}

// loadEntry is load for a test with no use for the entry's store
func (s *server) loadEntry(root *os.Root, f *os.File, m *fileMeta, key string) *entry {
	e, pending := s.load(root, f, m, key)
	s.storeEntry(e, pending)
	s.stores.Wait()
	return e
}

func openForLoad(t *testing.T, s *server, key string) (*os.Root, *os.File, *fileMeta) {
	t.Helper()
	root := s.root.Load().root
	f, err := root.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	m := s.newMeta(key, fi, "text/css")
	return root, f, &m
}

func TestLoadRejectsChangedFile(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.cache.activate()
	r, f, m := openForLoad(t, s, "app.css")
	replaceFile(t, filepath.Join(root, "app.css"), "body{color:red}")
	if s.loadEntry(r, f, m, "app.css") != nil || s.cache.count() != 0 {
		t.Error("expected a file replaced after it was opened not to be held")
	}
	if s.loadEntry(r, f, m, "missing.css") != nil {
		t.Error("expected a file that is gone not to be held")
	}
	// a file that shrank after it was stat'd can't be read in full
	r, f, m = openForLoad(t, s, "app.css")
	m.size += 10
	if s.loadEntry(r, f, m, "app.css") != nil {
		t.Error("expected a short read not to be held")
	}
	if s.cache.size.Load() != 0 || s.cache.files.Load() != 0 || s.watcher.Watched() != 0 {
		t.Error("expected every failed load to release its reservation")
	}
}

func TestValidator(t *testing.T) {
	root := newTestSite(t)
	path := filepath.Join(root, "app.css")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `"` + strconv.FormatInt(fi.ModTime().UnixNano(), 16) + "-" + strconv.FormatInt(fi.Size(), 16) + `"`
	disk := newTestServer(t, testOptions(root))
	held := newTestServer(t, testOptions(root))
	held.start()
	// the validator is the modification time and size alone, however the file is served
	for range 2 {
		for name, s := range map[string]*server{"disk": disk, "memory": held} {
			for _, method := range []string{http.MethodHead, http.MethodGet} {
				if got := get(t, s, method, "/app.css").Header.Get(headers.NameETag); got != want {
					t.Errorf("%s %s: expected validator %s got %s", name, method, want, got)
				}
			}
		}
	}
	m := disk.newMeta("app.css", fi, "text/css")
	if m.etag != want || m.weakETag != "W/"+want {
		t.Errorf("expected %s and its weak form, got %s and %s", want, m.etag, m.weakETag)
	}

	later := fi.ModTime().Add(time.Second)
	if err = os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	touched := get(t, disk, http.MethodGet, "/app.css").Header.Get(headers.NameETag)
	if touched == want {
		t.Error("expected a new validator for a new modification time")
	}
	writeFile(t, path, testCSS+" ")
	if err = os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := get(t, disk, http.MethodGet, "/app.css").Header.Get(headers.NameETag); got == touched {
		t.Error("expected a new validator for a new size")
	}
}

func TestBodyIsReadOnlyWhenNeeded(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.start()
	resp := get(t, s, http.MethodHead, "/app.css")
	etag, modified := resp.Header.Get(headers.NameETag), resp.Header.Get(headers.NameLastModified)
	if resp.StatusCode != http.StatusOK || etag == "" ||
		resp.Header.Get(headers.NameContentLength) != strconv.Itoa(len(testCSS)) {
		t.Fatalf("expected a complete HEAD response, got %d %v", resp.StatusCode, resp.Header)
	}
	requests := []struct {
		name   string
		status int
		hdrs   []string
	}{
		{"matching etag", http.StatusNotModified, []string{"If-None-Match", etag}},
		{"unmodified since", http.StatusNotModified, []string{"If-Modified-Since", modified}},
		{"failed precondition", http.StatusPreconditionFailed, []string{"If-Match", `"other"`}},
		{"range", http.StatusPartialContent, []string{"Range", "bytes=0-2"}},
		{"unsatisfiable range", http.StatusRequestedRangeNotSatisfiable, []string{"Range", "bytes=99-"}},
	}
	for _, test := range requests {
		if resp = get(t, s, http.MethodGet, "/app.css", test.hdrs...); resp.StatusCode != test.status {
			t.Errorf("%s: expected %d got %d", test.name, test.status, resp.StatusCode)
		}
		get(t, s, http.MethodHead, "/app.css", test.hdrs...)
	}
	if s.cache.count() != 0 || s.cache.files.Load() != 0 || s.watcher.Watched() != 0 {
		t.Fatalf("expected requests that need no body, or only part of one, to hold nothing; got %d",
			s.cache.count())
	}
	// the validator a HEAD returned still matches once a GET has loaded the file
	resp = get(t, s, http.MethodGet, "/app.css")
	if body(t, resp) != testCSS || resp.Header.Get(headers.NameETag) != etag || s.cache.count() != 1 {
		t.Error("expected a full GET to load the file under the same validator")
	}
}

func TestConcurrentLoadsShareOneRead(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.cache.activate()
	r, f, m := openForLoad(t, s, "app.css")
	shared := &entry{fileMeta: *m, key: "app.css", body: []byte(testCSS)}

	// hold the file's flight open, as a load in progress does
	release := make(chan struct{})
	leading := make(chan struct{})
	go s.loads.Do("app.css", func() (any, error) {
		close(leading)
		<-release
		return loaded{entry: shared}, nil
	})
	<-leading
	const followers = 16
	results := make(chan *entry, followers)
	for range followers {
		go func() {
			e, _ := s.load(r, f, m, "app.css")
			results <- e
		}()
	}
	// followers may only wait: none reserves capacity or allocates a body of its own
	time.Sleep(50 * time.Millisecond)
	if s.cache.files.Load() != 0 || s.cache.size.Load() != 0 {
		t.Errorf("expected waiting loads to reserve nothing, got %d files", s.cache.files.Load())
	}
	close(release)
	for range followers {
		if e := <-results; e != shared {
			t.Fatal("expected every waiting load to share the one that was in progress")
		}
	}
	// a shared entry describing a different file than the request opened is refused
	_, _, other := openForLoad(t, s, "index.html")
	go s.loads.Do("app.css", func() (any, error) { return loaded{entry: &entry{fileMeta: *other}}, nil })
	if !waitFor(5*time.Second, func() bool { return s.loadEntry(r, f, m, "app.css") != nil }) {
		t.Error("expected a load to succeed once no mismatched flight is in progress")
	}
}

func TestConcurrentColdMisses(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	s.start()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/app.css", nil)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Body.String() != testCSS {
				t.Errorf("unexpected response %d %q", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	// the store is made away from the requests, which were made without the helper that waits for it
	s.stores.Wait()
	if s.cache.count() != 1 || s.cache.files.Load() != 1 ||
		s.cache.size.Load() != entryCost("app.css", int64(len(testCSS))) {
		t.Errorf("expected one entry and no leaked reservations, got %d files %d bytes",
			s.cache.files.Load(), s.cache.size.Load())
	}
}

func TestLazyContent(t *testing.T) {
	root := newTestSite(t)
	s := newTestServer(t, testOptions(root))
	r, f, m := openForLoad(t, s, "app.css")
	// the cache is inactive, so the body can't be held and is read from the file
	l := &lazyContent{s: s, root: r, f: f, meta: m, key: "app.css"}
	if n, err := l.Seek(0, io.SeekEnd); err != nil || n != int64(len(testCSS)) {
		t.Errorf("expected the size from metadata, got %d %v", n, err)
	}
	if _, err := l.Seek(-1, io.SeekStart); err == nil {
		t.Error("expected an error seeking before the start")
	}
	if _, err := l.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := l.Seek(1, io.SeekCurrent); err != nil || n != 3 {
		t.Errorf("expected offset 3, got %d %v", n, err)
	}
	b, err := io.ReadAll(l)
	if err != nil || string(b) != testCSS[3:] {
		t.Errorf("expected %q from the file, got %q %v", testCSS[3:], b, err)
	}
	if _, err = l.Seek(1, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if b, _ = io.ReadAll(l); string(b) != testCSS[1:] {
		t.Errorf("expected %q after seeking the file, got %q", testCSS[1:], b)
	}

	// held, it is read from memory, from wherever the reader was positioned
	s.cache.activate()
	r, f, m = openForLoad(t, s, "app.css")
	l = &lazyContent{s: s, root: r, f: f, meta: m, key: "app.css"}
	if _, err = l.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(l)
	s.stores.Wait()
	if string(b) != testCSS[4:] || s.cache.count() != 1 {
		t.Errorf("expected %q from memory, got %q", testCSS[4:], b)
	}
	if n, err := l.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Errorf("expected EOF at the end of the body, got %d %v", n, err)
	}
	// a closed file fails the fallback seek rather than serving from the wrong offset
	r, f, m = openForLoad(t, s, "index.html")
	s.cache.retire()
	f.Close()
	l = &lazyContent{s: s, root: r, f: f, meta: m, key: "index.html"}
	if _, err = l.Read(make([]byte, 1)); err == nil {
		t.Error("expected an error reading a closed file")
	}
}

func TestConcurrentServeAndChange(t *testing.T) {
	root := newTestSite(t)
	o := testOptions(root)
	o.FileserverCache.RevalidationInterval = 1000 * 1000 // 1ms
	s := newTestServer(t, o)
	s.start()
	valid := map[string]bool{testCSS: true}
	versions := make([]string, 20)
	for i := range versions {
		versions[i] = strings.Repeat("v", i+1)
		valid[versions[i]] = true
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				r := httptest.NewRequest(http.MethodGet, "http://example.com/app.css", nil)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != http.StatusOK || !valid[w.Body.String()] {
					t.Errorf("unexpected response %d %q", w.Code, w.Body.String())
					return
				}
			}
		})
	}
	for _, v := range versions {
		replaceFile(t, filepath.Join(root, "app.css"), v)
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	last := versions[len(versions)-1]
	if !waitFor(5*time.Second, func() bool {
		return body(t, get(t, s, http.MethodGet, "/app.css")) == last
	}) {
		t.Error("expected the final version to be served once changes settle")
	}
}

func TestWellKnownIsServed(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, ".well-known", "security.txt"), "Contact: mailto:security@example.com")
	writeFile(t, filepath.Join(root, ".well-known", "acme-challenge", "token"), "token.thumbprint")
	writeFile(t, filepath.Join(root, ".well-known", ".secret"), "secret")
	writeFile(t, filepath.Join(root, "docs", ".well-known", "security.txt"), "nested")
	s := newTestServer(t, testOptions(root))
	s.start()
	tests := []struct {
		target, body string
		status       int
	}{
		{"/.well-known/security.txt", "Contact: mailto:security@example.com", http.StatusOK},
		{"/.well-known/acme-challenge/token", "token.thumbprint", http.StatusOK},
		{"/.well-known", "", http.StatusMovedPermanently},
		{"/.well-known/", "", http.StatusNotFound},
		{"/.well-known/.secret", "", http.StatusNotFound},
		{"/docs/.well-known/security.txt", "", http.StatusNotFound},
		{"/.env", "", http.StatusNotFound},
	}
	for range 2 {
		for _, test := range tests {
			resp := get(t, s, http.MethodGet, test.target)
			if resp.StatusCode != test.status || (test.status == http.StatusOK && body(t, resp) != test.body) {
				t.Errorf("%s: expected %d got %d", test.target, test.status, resp.StatusCode)
			}
		}
	}
	// the exemption is for serving it, not for advertising it
	if err := os.Remove(filepath.Join(root, "index.html")); err != nil {
		t.Fatal(err)
	}
	s.cache.purge()
	page := body(t, get(t, withDirectoryListing(s, s), http.MethodGet, "/"))
	if !strings.Contains(page, "docs/") || strings.Contains(page, ".well-known") {
		t.Errorf("expected a listing without the well-known directory:\n%s", page)
	}
}

func TestNotFoundFile(t *testing.T) {
	root := newTestSite(t)
	const page = "<h1>nothing here</h1>"
	writeFile(t, filepath.Join(root, "errors", "404.html"), page)
	missing := []string{"/nope", "/nope/", "/empty/", "/.env", "/docs/.hidden", "/app.css/", "/a/b/c.js"}

	o := testOptions(root)
	o.NotFoundFile = "/errors/404.html"
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	if o.NotFoundFile != "errors/404.html" || o.NotFoundStatus != http.StatusNotFound {
		t.Fatalf("expected a normalized file and a default status, got %q %d", o.NotFoundFile, o.NotFoundStatus)
	}
	s := newTestServer(t, o)
	s.start()
	for range 2 {
		for _, target := range missing {
			// whatever the request asked of the missing file is no business of the error page's
			resp := get(t, s, http.MethodGet, target, "Range", "bytes=0-3", "If-None-Match", "*")
			if resp.StatusCode != http.StatusNotFound || body(t, resp) != page {
				t.Errorf("%s: expected the error page with a 404, got %d", target, resp.StatusCode)
			}
			h := resp.Header
			if h.Get(headers.NameContentType) != contentTypeHTML || h.Get(headers.NameCacheControl) != headers.ValueNoCache ||
				h.Get(headers.NameETag) != "" || h.Get(headers.NameLastModified) != "" || h.Get(headers.NameAcceptRanges) != "" {
				t.Errorf("%s: expected an error page that can't be revalidated or reused, got %v", target, h)
			}
		}
	}
	if resp := get(t, s, http.MethodHead, "/nope"); resp.StatusCode != http.StatusNotFound || body(t, resp) != "" ||
		resp.Header.Get(headers.NameContentLength) != strconv.Itoa(len(page)) {
		t.Errorf("expected a HEAD to describe the error page, got %d %v", resp.StatusCode, resp.Header)
	}
	// what exists is unaffected, including the error page when asked for by its own path
	if resp := get(t, s, http.MethodGet, "/app.css"); resp.StatusCode != http.StatusOK || body(t, resp) != testCSS {
		t.Errorf("expected an existing file to be served, got %d", resp.StatusCode)
	}
	if resp := get(t, s, http.MethodGet, "/errors/404.html"); resp.StatusCode != http.StatusOK ||
		resp.Header.Get(headers.NameETag) == "" {
		t.Errorf("expected the error page to be an ordinary file at its own path, got %d", resp.StatusCode)
	}
	if resp := get(t, s, http.MethodGet, "/docs"); resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("expected a directory to still redirect, got %d", resp.StatusCode)
	}
	if resp := get(t, s, http.MethodPost, "/nope"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected a refused method to stay refused, got %d", resp.StatusCode)
	}
	// a listing still takes precedence for a directory that can be listed
	if resp := get(t, withDirectoryListing(s, s), http.MethodGet, "/empty/"); resp.StatusCode != http.StatusOK ||
		!strings.Contains(body(t, resp), "a.txt") {
		t.Errorf("expected a listing in place of the error page, got %d", resp.StatusCode)
	}

	// without the file, or with a directory in its place, a plain 404 rather than a loop
	for _, file := range []string{"errors/missing.html", "docs"} {
		o = testOptions(root)
		o.NotFoundFile, o.NotFoundStatus = file, http.StatusOK
		s = newTestServer(t, o)
		resp := get(t, s, http.MethodGet, "/nope")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body(t, resp), http.StatusText(http.StatusNotFound)) {
			t.Errorf("%s: expected a plain 404, got %d", file, resp.StatusCode)
		}
	}
}

func TestNotFoundFileForSinglePageApplication(t *testing.T) {
	root := newTestSite(t)
	writeFile(t, filepath.Join(root, "index.html"), "<html>"+strings.Repeat("<app-root></app-root>", 100)+"</html>")
	app, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	o := testOptions(root)
	o.NotFoundFile, o.NotFoundStatus = "index.html", http.StatusOK
	o.CacheControl = testCacheControl
	s := newTestServer(t, o)
	s.start()
	h := eh.HandleCompression(s, s.compressible)
	// every route of the application is the application, served as the file it is
	etag := get(t, h, http.MethodGet, "/").Header.Get(headers.NameETag)
	for _, target := range []string{"/dashboard", "/users/42/edit", "/settings/"} {
		resp := get(t, h, http.MethodGet, target)
		if resp.StatusCode != http.StatusOK || body(t, resp) != string(app) || resp.Header.Get(headers.NameETag) != etag {
			t.Errorf("%s: expected the application with a 200, got %d", target, resp.StatusCode)
		}
		if resp.Header.Get(headers.NameCacheControl) != testCacheControl {
			t.Errorf("%s: expected the file's own Cache-Control", target)
		}
		if resp = get(t, h, http.MethodGet, target, "If-None-Match", etag); resp.StatusCode != http.StatusNotModified {
			t.Errorf("%s: expected the application to revalidate, got %d", target, resp.StatusCode)
		}
		resp = get(t, h, http.MethodGet, target, headers.NameAcceptEncoding, "gzip")
		if enc := resp.Header.Get(headers.NameContentEncoding); enc != "gzip" || decode(t, enc, body(t, resp)) != string(app) {
			t.Errorf("%s: expected the application's held rendition, got %q", target, enc)
		}
	}
	if s.cache.count() != 2 {
		t.Errorf("expected one held file and one rendition to serve every route, got %d", s.cache.count())
	}
	if resp := get(t, h, http.MethodGet, "/app.css"); body(t, resp) != testCSS {
		t.Error("expected an existing file to be served in place of the application")
	}
}

func TestEvictionKeepsServing(t *testing.T) {
	root := newTestSite(t)
	for i := range 20 {
		writeFile(t, filepath.Join(root, "pages", strconv.Itoa(i), "page.txt"), "page "+strconv.Itoa(i))
	}
	o := testOptions(root)
	o.FileserverCache.MaxFiles = 5
	s := newTestServer(t, o)
	s.start()
	for range 3 {
		for i := range 20 {
			// the home page is read between every page, so it is never the least recently used
			if body(t, get(t, s, http.MethodGet, "/")) != testHome {
				t.Fatal("expected the home page")
			}
			target := "/pages/" + strconv.Itoa(i) + "/page.txt"
			if got := body(t, get(t, s, http.MethodGet, target)); got != "page "+strconv.Itoa(i) {
				t.Fatalf("%s: got %q", target, got)
			}
		}
	}
	if s.cache.count() != 5 || s.cache.files.Load() != 5 || s.watcher.Watched() > 5 {
		t.Errorf("expected the limit to hold, got %d held over %d watches", s.cache.count(), s.watcher.Watched())
	}
	if s.cache.held("index.html") == nil {
		t.Error("expected the file in constant use to have stayed held")
	}
	if responses(s, statusHit, providers.Identity) < 59 {
		t.Errorf("expected the home page to be a hit every time after the first, got %v",
			responses(s, statusHit, providers.Identity))
	}
	if testutil.ToFloat64(s.cache.metrics.evictions) == 0 {
		t.Error("expected evictions to be counted")
	}
}

func TestFailLogsUnexpectedErrors(t *testing.T) {
	s := newTestServer(t, testOptions(newTestSite(t)))
	w := httptest.NewRecorder()
	s.fail(w, httptest.NewRequest(http.MethodGet, "http://example.com/app.css", nil), "app.css",
		os.ErrPermission, false)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected an unreadable file to look absent, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(http.StatusText(http.StatusNotFound))) {
		t.Error("expected the status text as the error body")
	}
}
