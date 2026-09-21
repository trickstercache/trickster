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

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const staticTestConfig = "testdata/configs/static.yaml"

// staticBigCSS is repetitive enough to be worth encoding, and within the 4096 bytes
// that the test config lets the site backend hold
var staticBigCSS = strings.Repeat("body { color: red; margin: 0; padding: 0 }\n", 60)

func writeStaticFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

// staticHarness writes the two sites the config serves and points it at them
func staticHarness(t *testing.T, mods ...func(*tkconfig.Config)) (h tricksterHarness, site, private string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	site, private = filepath.Join(base, "site"), filepath.Join(base, "private")
	writeStaticFile(t, filepath.Join(site, "index.html"), "<h1>home</h1>")
	writeStaticFile(t, filepath.Join(site, "app.css"), "body{}")
	writeStaticFile(t, filepath.Join(site, "page.custom"), "custom")
	writeStaticFile(t, filepath.Join(site, "big.bin"), strings.Repeat("x", 8192))
	writeStaticFile(t, filepath.Join(site, "docs", "index.html"), "<h1>docs</h1>")
	writeStaticFile(t, filepath.Join(site, "empty", "a.txt"), "a")
	writeStaticFile(t, filepath.Join(site, ".env"), "SECRET=1")
	writeStaticFile(t, filepath.Join(site, ".git", "config"), "secret")
	writeStaticFile(t, filepath.Join(site, "big.css"), staticBigCSS)
	writeStaticFile(t, filepath.Join(site, ".well-known", "security.txt"), "Contact: mailto:security@example.com")
	writeStaticFile(t, filepath.Join(site, ".well-known", ".secret"), "secret")
	writeStaticFile(t, filepath.Join(private, "errors", "404.html"), "<h1>nothing here</h1>")
	writeStaticFile(t, filepath.Join(private, "home.html"), "<h1>private</h1>")
	writeStaticFile(t, filepath.Join(private, "reports", "q1.txt"), "q1")
	writeStaticFile(t, filepath.Join(private, "reports", ".draft"), "draft")

	roots := func(c *tkconfig.Config) {
		c.Backends["site"].Static.Root = site
		c.Backends["private"].Static.Root = private
		c.Backends["app"].Static.Root = site
	}
	ports, release := portutil.Reserve(t, 3)
	h = tricksterHarness{
		ConfigPath: writeTestConfig(t, staticTestConfig, ports[0], ports[1], ports[2], 0, 0, 0, 0,
			append([]func(*tkconfig.Config){roots}, mods...)...),
		BaseAddr:     fmt.Sprintf("127.0.0.1:%d", ports[0]),
		MetricsAddr:  fmt.Sprintf("127.0.0.1:%d", ports[1]),
		MgmtAddr:     fmt.Sprintf("127.0.0.1:%d", ports[2]),
		releasePorts: release,
	}
	htpwPath := filepath.Join(base, "htpasswd")
	writeHtpasswd(t, htpwPath, "test", "password")
	rewriteGeneratedConfig(t, h.ConfigPath, "testdata/configs/htpasswd", htpwPath)
	return h, site, private
}

// staticMetric reads one series. The daemon runs in this process, so a series outlives the
// test that first touched it, and tests compare readings rather than expect absolute values.
func staticMetric(t *testing.T, h tricksterHarness, series string) float64 {
	t.Helper()
	for _, line := range checkTricksterMetrics(t, h.MetricsAddr) {
		if value, ok := strings.CutPrefix(line, series+" "); ok {
			f, err := strconv.ParseFloat(value, 64)
			require.NoError(t, err)
			return f
		}
	}
	return 0
}

func TestStatic_ServesFiles(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)

	tests := []struct {
		path        string
		status      int
		body        string
		contentType string
	}{
		{"/", http.StatusOK, "<h1>home</h1>", "text/html; charset=utf-8"},
		{"/index.html", http.StatusOK, "<h1>home</h1>", "text/html; charset=utf-8"},
		{"/docs/", http.StatusOK, "<h1>docs</h1>", "text/html; charset=utf-8"},
		{"/app.css", http.StatusOK, "body{}", "text/css; charset=utf-8"},
		{"/page.custom", http.StatusOK, "custom", "text/x-custom"},
		{"/site/docs/", http.StatusOK, "<h1>docs</h1>", "text/html; charset=utf-8"},
		// directory listing is off, so a directory with no default file is absent
		{"/empty/", http.StatusNotFound, "", ""},
		{"/nope.html", http.StatusNotFound, "", ""},
		{"/.env", http.StatusNotFound, "", ""},
		{"/.git/config", http.StatusNotFound, "", ""},
		{"/site/.env", http.StatusNotFound, "", ""},
	}
	// twice, so each file is answered from disk and then from memory
	for range 2 {
		for _, test := range tests {
			resp, body := h.do(t, test.path)
			require.Equal(t, test.status, resp.StatusCode, test.path)
			require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), test.path)
			if test.status != http.StatusOK {
				continue
			}
			require.Equal(t, test.body, string(body), test.path)
			require.Equal(t, test.contentType, resp.Header.Get("Content-Type"), test.path)
			require.NotEmpty(t, resp.Header.Get("Etag"), test.path)
			require.NotEmpty(t, resp.Header.Get("Last-Modified"), test.path)
		}
	}

	resp, _ := h.do(t, "/")
	require.Equal(t, "public, max-age=60", resp.Header.Get("Cache-Control"))
	resp, _ = h.do(t, "/app.css")
	require.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))

	resp, _ = h.do(t, "/", func(o *requestOptions) { o.method = http.MethodPost })
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	require.Equal(t, "GET, HEAD, OPTIONS", resp.Header.Get("Allow"))
	resp, body := h.do(t, "/", func(o *requestOptions) { o.method = http.MethodHead })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, body)
}

func TestStatic_RedirectsDirectories(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for path, location := range map[string]string{
		"/docs":          "docs/",
		"/docs?a=b":      "docs/?a=b",
		"/site/docs":     "docs/",
		"/private/nope/": "",
	} {
		req, err := http.NewRequest(http.MethodGet, "http://"+h.BaseAddr+path, nil)
		require.NoError(t, err)
		req.SetBasicAuth("test", "password")
		resp, err := client.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		if location == "" {
			require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
			continue
		}
		require.Equal(t, http.StatusMovedPermanently, resp.StatusCode, path)
		require.Equal(t, location, resp.Header.Get("Location"), path)
	}
	// followed, the relative redirect lands on the default file under either route
	for _, path := range []string{"/docs", "/site/docs"} {
		resp, body := h.do(t, path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Equal(t, "<h1>docs</h1>", string(body), path)
	}
}

func TestStatic_ConditionalRangeAndEncoding(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)
	for _, path := range []string{"/index.html", "/big.bin"} {
		// asked for first, so the validator comes from a file that was never read
		head, _ := h.do(t, path, func(o *requestOptions) { o.method = http.MethodHead })
		resp, full := h.do(t, path)
		require.Equal(t, head.Header.Get("Etag"), resp.Header.Get("Etag"), path)
		etag, modified := resp.Header.Get("Etag"), resp.Header.Get("Last-Modified")
		require.True(t, strings.HasPrefix(etag, `"`), "expected a strong etag, got %s", etag)

		resp, body := h.do(t, path, withHeader("If-None-Match", etag))
		require.Equal(t, http.StatusNotModified, resp.StatusCode, path)
		require.Empty(t, body)
		resp, _ = h.do(t, path, withHeader("If-Modified-Since", modified))
		require.Equal(t, http.StatusNotModified, resp.StatusCode, path)

		resp, body = h.do(t, path, withHeader("Range", "bytes=2-5"))
		require.Equal(t, http.StatusPartialContent, resp.StatusCode, path)
		require.Equal(t, string(full[2:6]), string(body), path)
		require.Equal(t, etag, resp.Header.Get("Etag"), path)
	}

	const (
		missSeries  = `trickster_fileserver_responses_total{backend_name="site",cache_status="kmiss",encoding="gzip"}`
		hitSeries   = `trickster_fileserver_responses_total{backend_name="site",cache_status="hit",encoding="gzip"}`
		usageSeries = `trickster_fileserver_cache_usage_objects{backend_name="site"}`
	)
	misses, hits := staticMetric(t, h, missSeries), staticMetric(t, h, hitSeries)
	// a rendition is streamed as it is made, so without a length; held, it is sent with its own
	for i := range 2 {
		resp, body := h.do(t, "/big.css", withHeader("Accept-Encoding", "gzip"))
		require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		require.Equal(t, staticBigCSS, string(body), "expected a body that was encoded exactly once")
		require.True(t, strings.HasPrefix(resp.Header.Get("Etag"), `W/"`), "expected a weak etag when encoded")
		require.Equal(t, "Accept-Encoding", resp.Header.Get("Vary"))
		if i == 0 {
			// the rendition is held away from the request, which a moment's grace allows for
			require.Eventually(t, func() bool { return staticMetric(t, h, usageSeries) >= 2 },
				5*time.Second, 20*time.Millisecond, "the rendition was never held")
			continue
		}
		require.Positive(t, resp.ContentLength)
		require.Less(t, resp.ContentLength, int64(len(staticBigCSS)))
	}
	require.Equal(t, misses+1, staticMetric(t, h, missSeries), "expected the rendition to be made once")
	require.Equal(t, hits+1, staticMetric(t, h, hitSeries), "expected the rendition to be reused")
	// weights are honored: gzip is held and outweighs the rest, then is refused outright
	resp, body := h.do(t, "/big.css", withHeader("Accept-Encoding", "zstd;q=0.5, GZIP;q=0.9, br;q=0.1"))
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
	require.Equal(t, staticBigCSS, string(body))
	resp, _ = h.do(t, "/big.css", withHeader("Accept-Encoding", "gzip;q=0, deflate;q=0.2, br;q=0.7"))
	require.Equal(t, "br", resp.Header.Get("Content-Encoding"), "expected a new rendition in the highest weight")
	// byte ranges address the stored file, so a partial response is never encoded
	resp, body = h.do(t, "/big.css", withHeader("Accept-Encoding", "gzip"), withHeader("Range", "bytes=0-3"))
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	require.Equal(t, staticBigCSS[:4], string(body))
	// a file too small for encoding to shrink is sent as it is
	resp, body = h.do(t, "/index.html", withHeader("Accept-Encoding", "gzip"))
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	require.Equal(t, "<h1>home</h1>", string(body))

	require.Equal(t, float64(100), staticMetric(t, h, `trickster_fileserver_cache_max_usage_objects{backend_name="site"}`))
	require.Positive(t, staticMetric(t, h, `trickster_fileserver_cache_usage_objects{backend_name="site"}`))
	require.Positive(t, staticMetric(t, h, `trickster_fileserver_cache_usage_bytes{backend_name="site"}`))
}

func TestStatic_WellKnown(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)
	for _, path := range []string{"/.well-known/security.txt", "/site/.well-known/security.txt"} {
		resp, body := h.do(t, path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Equal(t, "Contact: mailto:security@example.com", string(body), path)
	}
	for _, path := range []string{"/.well-known/.secret", "/.well-known/", "/.env", "/docs/.well-known/security.txt"} {
		resp, _ := h.do(t, path)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
	}
}

func TestStatic_NotFoundFile(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)
	// a single-page application: its routes are the application, as the file that it is
	home, _ := h.do(t, "/app/")
	for _, path := range []string{"/app/dashboard", "/app/users/42/edit", "/app/.env"} {
		resp, body := h.do(t, path)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.Equal(t, "<h1>home</h1>", string(body), path)
		require.Equal(t, home.Header.Get("Etag"), resp.Header.Get("Etag"), path)
	}
	resp, body := h.do(t, "/app/app.css")
	require.Equal(t, "body{}", string(body), "expected an existing file in place of the application")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// an error page: a 404 that can't be mistaken for, or reused as, the missing file
	auth := func(o *requestOptions) {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		r.SetBasicAuth("test", "password")
		withHeader("Authorization", r.Header.Get("Authorization"))(o)
	}
	resp, body = h.do(t, "/private/nope.html", auth, withHeader("Range", "bytes=0-3"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "<h1>nothing here</h1>", string(body))
	require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	require.Empty(t, resp.Header.Get("Etag"))
	// and only for those allowed to know what is missing
	resp, body = h.do(t, "/private/nope.html")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.NotContains(t, string(body), "nothing here")
	// a backend without one answers plainly
	resp, body = h.do(t, "/nope.html")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NotContains(t, string(body), "<h1>")
}

func TestStatic_EvictionKeepsServing(t *testing.T) {
	h, site, _ := staticHarness(t)
	for i := range 12 {
		writeStaticFile(t, filepath.Join(site, "pages", fmt.Sprintf("%d.txt", i)), fmt.Sprintf("page %d", i))
	}
	h.start(t)
	const evictionSeries = `trickster_fileserver_cache_events_total{backend_name="app",event="eviction"}`
	evictions := staticMetric(t, h, evictionSeries)
	// the app backend holds 3 files, so serving 12 of them turns its cache over repeatedly
	for range 2 {
		for i := range 12 {
			resp, body := h.do(t, fmt.Sprintf("/app/pages/%d.txt", i))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, fmt.Sprintf("page %d", i), string(body))
		}
	}
	// 24 loads into room for 3, so nearly all of them made room by evicting. It is not exactly
	// all: a load that finds the cache busy with the one before it is served without being held.
	require.Eventually(t, func() bool {
		return staticMetric(t, h, `trickster_fileserver_cache_usage_objects{backend_name="app"}`) == 3
	}, 5*time.Second, 20*time.Millisecond, "expected the cache to be full, and no fuller")
	require.GreaterOrEqual(t, staticMetric(t, h, evictionSeries)-evictions, float64(12))
	require.LessOrEqual(t, staticMetric(t, h, evictionSeries)-evictions, float64(21))
}

func TestStatic_Authenticator(t *testing.T) {
	h, _, _ := staticHarness(t)
	h.start(t)
	auth := func(user, pass string) requestOption {
		return func(o *requestOptions) {
			r, _ := http.NewRequest(http.MethodGet, "/", nil)
			r.SetBasicAuth(user, pass)
			withHeader("Authorization", r.Header.Get("Authorization"))(o)
		}
	}
	for _, path := range []string{"/private/", "/private/reports/q1.txt", "/private/nope", "/private/.draft"} {
		resp, _ := h.do(t, path)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "expected 401 with no credentials: "+path)
		resp, _ = h.do(t, path, auth("test", "wrong"))
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "expected 401 with bad credentials: "+path)
	}
	resp, body := h.do(t, "/private/", auth("test", "password"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "<h1>private</h1>", string(body), "expected the configured default file")
	resp, body = h.do(t, "/private/reports/q1.txt", auth("test", "password"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "q1", string(body))

	// this backend lists directories that have no default file, minus dotfiles
	resp, body = h.do(t, "/private/reports/", auth("test", "password"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `<a href="./q1.txt">q1.txt</a>`)
	require.NotContains(t, string(body), ".draft")
	resp, _ = h.do(t, "/private/reports/.draft", auth("test", "password"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	// the open backend is unaffected by its neighbor's authenticator
	resp, _ = h.do(t, "/site/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func requireStaticBody(t *testing.T, h tricksterHarness, path string, status int, want string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, body := h.do(t, path)
		assert.Equal(collect, status, resp.StatusCode)
		if status == http.StatusOK {
			assert.Equal(collect, want, string(body))
		}
	}, 10*time.Second, 50*time.Millisecond, "%s never served %d %q", path, status, want)
}

func TestStatic_ChangesOnDiskAreServed(t *testing.T) {
	h, site, _ := staticHarness(t)
	h.start(t)
	for range 2 {
		requireStaticBody(t, h, "/app.css", http.StatusOK, "body{}")
	}
	writeStaticFile(t, filepath.Join(site, "app.css"), "body{color:red}")
	requireStaticBody(t, h, "/app.css", http.StatusOK, "body{color:red}")

	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("rewritten in place"), 0o644))
	requireStaticBody(t, h, "/", http.StatusOK, "rewritten in place")

	require.NoError(t, os.Remove(filepath.Join(site, "app.css")))
	requireStaticBody(t, h, "/app.css", http.StatusNotFound, "")

	writeStaticFile(t, filepath.Join(site, "empty", "index.html"), "no longer empty")
	requireStaticBody(t, h, "/empty/", http.StatusOK, "no longer empty")
}

func TestStatic_SurvivesReload(t *testing.T) {
	// Drop prior SIGHUP handlers so this test owns the only live receiver.
	signal.Reset(syscall.SIGHUP)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, site, _ := staticHarness(t)
	if h.releasePorts != nil {
		h.releasePorts()
	}
	runTrickster(t, ctx, "-config", h.ConfigPath)
	waitForTrickster(t, h.MetricsAddr)
	requireStaticBody(t, h, "/", http.StatusOK, "<h1>home</h1>")

	rewriteGeneratedConfig(t, h.ConfigPath, "public, max-age=60", "public, max-age=90")
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP), "failed to send SIGHUP for in-process reload")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, _ := h.do(t, "/")
		assert.Equal(collect, "public, max-age=90", resp.Header.Get("Cache-Control"))
	}, 15*time.Second, 100*time.Millisecond, "the reloaded cache_control was never served")

	// the reloaded backend holds files in memory and still notices changes
	for range 2 {
		requireStaticBody(t, h, "/", http.StatusOK, "<h1>home</h1>")
	}
	writeStaticFile(t, filepath.Join(site, "index.html"), "after reload")
	requireStaticBody(t, h, "/", http.StatusOK, "after reload")
}

func TestStatic_InvalidConfigs(t *testing.T) {
	tests := []struct {
		name, contains string
		mod            func(*tkconfig.Config)
	}{
		{"paths", `option "paths" is not supported`, func(c *tkconfig.Config) {
			c.Backends["site"].Paths = po.List{{Path: "/", HandlerName: "static"}}
		}},
		{"request rewriter", `option "req_rewriter_name" is not supported`, func(c *tkconfig.Config) {
			c.Backends["site"].ReqRewriterName = "rewriter1"
		}},
		{"origin url", `option "origin_url" is not supported`, func(c *tkconfig.Config) {
			c.Backends["site"].OriginURL = "http://127.0.0.1:9090"
		}},
		{"missing static options", "missing static options", func(c *tkconfig.Config) {
			c.Backends["site"].Static = nil
		}},
		{"missing root", "static.root is required", func(c *tkconfig.Config) {
			c.Backends["site"].Static.Root = ""
		}},
		{"root does not exist", "invalid static.root", func(c *tkconfig.Config) {
			c.Backends["site"].Static.Root = filepath.Join(t.TempDir(), "missing")
		}},
		{"static options on a proxy", `option "static" is not supported`, func(c *tkconfig.Config) {
			c.Backends["private"].Provider = "reverseproxy"
			c.Backends["private"].OriginURL = "http://127.0.0.1:9090"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, _, _ := staticHarness(t, test.mod)
			if h.releasePorts != nil {
				h.releasePorts()
			}
			startTrickster(t, context.Background(), expectedStartError{ErrorContains: &test.contains},
				"-config", h.ConfigPath)
		})
	}
}
