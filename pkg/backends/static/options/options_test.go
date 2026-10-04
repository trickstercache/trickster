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

package options

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestNew(t *testing.T) {
	o := New()
	if o.DefaultFile != DefaultDefaultFile || o.CacheControl != "" ||
		o.DirectoryListing || o.FileserverCache == nil {
		t.Error("expected default options with directory listing off")
	}
	mc := o.FileserverCache
	if mc.Disabled || mc.MaxFileSizeBytes != DefaultMaxFileSizeBytes ||
		mc.MaxSizeBytes != DefaultMaxSizeBytes || mc.MaxFiles != DefaultMaxFiles ||
		time.Duration(mc.RevalidationInterval) != DefaultRevalidationInterval {
		t.Error("expected default fileserver cache options")
	}
}

func TestClone(t *testing.T) {
	var o *Options
	if o.Clone() != nil {
		t.Error("expected a nil clone of nil options")
	}
	var mc *FileserverCacheOptions
	if mc.Clone() != nil {
		t.Error("expected a nil clone of nil fileserver cache options")
	}
	o = New()
	o.ResponseHeaders = map[string]string{"X-Test": "1"}
	o.MIMETypes = map[string]string{".a": "text/plain"}
	o.CacheControlByExtension = map[string]string{".a": "no-store"}
	c := o.Clone()
	c.ResponseHeaders["X-Test"] = "2"
	c.MIMETypes[".a"] = "text/html"
	c.CacheControlByExtension[".a"] = "private"
	c.FileserverCache.Disabled = true
	if o.ResponseHeaders["X-Test"] != "1" || o.MIMETypes[".a"] != "text/plain" ||
		o.CacheControlByExtension[".a"] != "no-store" || o.FileserverCache.Disabled {
		t.Error("expected a clone to be independent of its source")
	}
}

func TestInitialize(t *testing.T) {
	var o *Options
	if err := o.Initialize(); err != nil {
		t.Error(err)
	}
	o = &Options{
		Root:                    "relative/site",
		MIMETypes:               map[string]string{"MD": "text/markdown", ".Txt": "text/plain"},
		CacheControlByExtension: map[string]string{" js ": "no-store"},
	}
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(o.Root) || !strings.HasSuffix(o.Root, filepath.Join("relative", "site")) {
		t.Errorf("expected an absolute root, got %q", o.Root)
	}
	if o.DefaultFile != DefaultDefaultFile || o.FileserverCache == nil {
		t.Error("expected defaults for unset fields")
	}
	if o.MIMETypes[".md"] == "" || o.MIMETypes[".txt"] == "" || o.CacheControlByExtension[".js"] == "" {
		t.Errorf("expected normalized extensions, got %v %v", o.MIMETypes, o.CacheControlByExtension)
	}
	for in, want := range map[string]string{
		"/errors/404.html": "errors/404.html", "index.html": "index.html", "a/../b//c.html": "b/c.html",
		"../../etc/passwd": "etc/passwd",
	} {
		o = &Options{Root: "site", NotFoundFile: in}
		if err := o.Initialize(); err != nil || o.NotFoundFile != want || o.NotFoundStatus != 404 {
			t.Errorf("expected %q to normalize to %q with a 404, got %q %d", in, want, o.NotFoundFile, o.NotFoundStatus)
		}
	}
	o = &Options{Root: "site", NotFoundFile: "index.html", NotFoundStatus: 200}
	if err := o.Initialize(); err != nil || o.NotFoundStatus != 200 {
		t.Errorf("expected a configured status to be kept, got %d", o.NotFoundStatus)
	}
	if NormalizeExtension("") != "" {
		t.Error("expected an empty extension to stay empty")
	}
}

func TestValidate(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := func(mods ...func(*Options)) *Options {
		o := New()
		o.Root = root
		for _, mod := range mods {
			mod(o)
		}
		return o
	}
	var nilOptions *Options
	if err := nilOptions.Validate(); !errors.Is(err, ErrMissingRoot) {
		t.Errorf("expected ErrMissingRoot for nil options, got %v", err)
	}
	if err := valid().Validate(); err != nil {
		t.Errorf("expected valid options, got %v", err)
	}
	if err := valid(func(o *Options) {
		o.MIMETypes = map[string]string{".md": "text/markdown; charset=utf-8"}
		o.CacheControlByExtension = map[string]string{".js": "no-store"}
		o.ResponseHeaders = map[string]string{"X-Frame-Options": "DENY"}
		o.FileserverCache = &FileserverCacheOptions{Disabled: true}
		o.NotFoundFile, o.NotFoundStatus = "errors/404.html", 404
	}).Validate(); err != nil {
		t.Errorf("expected valid options, got %v", err)
	}
	if err := valid(func(o *Options) { o.FileserverCache = nil }).Validate(); err != nil {
		t.Errorf("expected nil fileserver cache options to be valid, got %v", err)
	}

	tests := []struct {
		name     string
		mod      func(*Options)
		expected error
	}{
		{"empty root", func(o *Options) { o.Root = " " }, ErrMissingRoot},
		{"missing root", func(o *Options) { o.Root = filepath.Join(root, "missing") }, nil},
		{"root is a file", func(o *Options) { o.Root = file }, nil},
		{"empty default file", func(o *Options) { o.DefaultFile = "" }, ErrInvalidDefaultFile},
		{"dot default file", func(o *Options) { o.DefaultFile = ".index" }, ErrInvalidDefaultFile},
		{"default file path", func(o *Options) { o.DefaultFile = "a/index.html" }, ErrInvalidDefaultFile},
		{"default file backslash", func(o *Options) { o.DefaultFile = `a\index.html` }, ErrInvalidDefaultFile},
		{"empty mime extension", func(o *Options) { o.MIMETypes = map[string]string{"": "text/plain"} }, nil},
		{"invalid mime type", func(o *Options) { o.MIMETypes = map[string]string{".a": "not a type"} }, nil},
		{"dot cache control extension", func(o *Options) {
			o.CacheControlByExtension = map[string]string{".": "no-store"}
		}, nil},
		{"empty header name", func(o *Options) { o.ResponseHeaders = map[string]string{"": "x"} }, nil},
		{"invalid header name", func(o *Options) { o.ResponseHeaders = map[string]string{"X Bad": "x"} }, nil},
		{"not found status alone", func(o *Options) { o.NotFoundStatus = 200 }, ErrInvalidNotFoundCode},
		{"not found status", func(o *Options) { o.NotFoundFile, o.NotFoundStatus = "404.html", 500 }, ErrInvalidNotFoundCode},
		{"not found dotfile", func(o *Options) { o.NotFoundFile, o.NotFoundStatus = ".404.html", 404 }, ErrInvalidNotFoundFile},
		{"not found dot segment", func(o *Options) { o.NotFoundFile, o.NotFoundStatus = "a/.b/404.html", 404 }, ErrInvalidNotFoundFile},
		{"not found backslash", func(o *Options) { o.NotFoundFile, o.NotFoundStatus = `a\404.html`, 404 }, ErrInvalidNotFoundFile},
		{"max file size", func(o *Options) { o.FileserverCache.MaxFileSizeBytes = 0 }, ErrInvalidMaxFileSize},
		{"max size", func(o *Options) { o.FileserverCache.MaxSizeBytes = 1 }, ErrInvalidMaxSize},
		{"max files", func(o *Options) { o.FileserverCache.MaxFiles = 0 }, ErrInvalidMaxFiles},
		{"interval", func(o *Options) { o.FileserverCache.RevalidationInterval = 0 }, ErrInvalidRevalInterval},
	}
	for _, test := range tests {
		err := valid(test.mod).Validate()
		if err == nil {
			t.Errorf("%s: expected an error", test.name)
		} else if test.expected != nil && !errors.Is(err, test.expected) {
			t.Errorf("%s: expected %v got %v", test.name, test.expected, err)
		}
	}
}

func TestUnmarshalYAML(t *testing.T) {
	var o Options
	err := yaml.Unmarshal([]byte(`
root: /var/www
cache_control: no-cache
directory_listing: true
cache:
  max_file_size_bytes: 2048
`), &o)
	if err != nil {
		t.Fatal(err)
	}
	if o.Root != "/var/www" || !o.DirectoryListing || o.DefaultFile != DefaultDefaultFile {
		t.Errorf("expected configured values over defaults, got %+v", o)
	}
	if o.CacheControl != "no-cache" {
		t.Errorf("expected the configured cache_control, got %q", o.CacheControl)
	}
	mc := o.FileserverCache
	if mc.MaxFileSizeBytes != 2048 || mc.MaxSizeBytes != DefaultMaxSizeBytes ||
		mc.MaxFiles != DefaultMaxFiles ||
		mc.RevalidationInterval != timeconv.Duration(DefaultRevalidationInterval) {
		t.Errorf("expected fileserver cache values over defaults, got %+v", mc)
	}

	o = Options{}
	if err = yaml.Unmarshal([]byte("root: /var/www\n"), &o); err != nil {
		t.Fatal(err)
	}
	if o.CacheControl != "" || o.FileserverCache == nil {
		t.Errorf("expected defaults for unset fields, got %+v", o)
	}
	if err = yaml.Unmarshal([]byte("root: [1]\n"), &o); err == nil {
		t.Error("expected an error for a mistyped root")
	}
	if err = yaml.Unmarshal([]byte("cache:\n  disabled: [1]\n"), &o); err == nil {
		t.Error("expected an error for a mistyped fileserver cache field")
	}
}
