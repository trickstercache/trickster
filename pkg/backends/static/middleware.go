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
	"html"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// The handlers in this file are optional features. Each wraps the file server
// only when its option is configured, so an unused feature costs nothing.

// withResponseHeaders attaches the configured headers to every response
func withResponseHeaders(custom map[string]string, next http.Handler) http.Handler {
	if len(custom) == 0 {
		return next
	}
	canonical := make(map[string][]string, len(custom))
	for k, v := range custom {
		canonical[http.CanonicalHeaderKey(k)] = []string{v}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		maps.Copy(h, canonical)
		next.ServeHTTP(w, r)
	})
}

// withDirectoryListing answers a directory that has no default file with a
// listing of its contents; every other request passes through to next.
func withDirectoryListing(s *server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		name, dirRequest, ok := resolve(r.URL.Path)
		if !ok || !dirRequest || s.hasDefaultFile(name) || !s.list(w, r, name) {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *server) hasDefaultFile(dir string) bool {
	key := s.defaultFileIn(dir)
	if s.cache.get(key, providers.Identity) != nil {
		return true
	}
	fi, err := s.root.Load().root.Stat(key)
	return err == nil && fi.Mode().IsRegular()
}

// list writes the listing and reports whether name was a listable directory
func (s *server) list(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := s.root.Load().root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return false
	}
	type item struct {
		name    string
		size    int64
		modTime time.Time
		dir     bool
	}
	items := make([]item, 0, len(entries))
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		// stat through the root so a symlink is described by its target
		fi, err := s.root.Load().root.Stat(childName(name, de.Name()))
		if err != nil || (!fi.IsDir() && !fi.Mode().IsRegular()) {
			continue
		}
		items = append(items, item{de.Name(), fi.Size(), fi.ModTime(), fi.IsDir()})
	}
	slices.SortFunc(items, func(a, b item) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})

	title := html.EscapeString("Index of " + r.URL.Path)
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(title)
	b.WriteString("</title>\n</head>\n<body>\n<h1>")
	b.WriteString(title)
	b.WriteString("</h1>\n<table>\n<tr><th align=\"left\">Name</th>" +
		"<th align=\"left\">Last Modified</th><th align=\"right\">Size</th></tr>\n")
	if name != rootName {
		b.WriteString("<tr><td><a href=\"../\">../</a></td><td></td><td></td></tr>\n")
	}
	for _, it := range items {
		display, size := it.name, strconv.FormatInt(it.size, 10)
		if it.dir {
			display, size = display+"/", "-"
		}
		href := url.URL{Path: "./" + display}
		b.WriteString("<tr><td><a href=\"")
		b.WriteString(html.EscapeString(href.String()))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(display))
		b.WriteString("</a></td><td>")
		b.WriteString(it.modTime.UTC().Format(time.RFC1123))
		b.WriteString("</td><td align=\"right\">")
		b.WriteString(size)
		b.WriteString("</td></tr>\n")
	}
	b.WriteString("</table>\n</body>\n</html>\n")

	h := w.Header()
	h.Set(headers.NameContentType, contentTypeHTML)
	// a listing is generated per request and has no validator to revalidate with
	h.Set(headers.NameCacheControl, headers.ValueNoCache)
	h.Set(headers.NameContentLength, strconv.Itoa(b.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b.Bytes())
	}
	return true
}

func childName(dir, name string) string {
	if dir == rootName {
		return name
	}
	return dir + "/" + name
}
