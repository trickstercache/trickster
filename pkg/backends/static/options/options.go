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

// Package options provides the settings for the static file server backend.
package options

import (
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

// Options configures a static file server backend
type Options struct {
	// Root is the path of the directory holding the content to serve; it is required
	Root string `yaml:"root,omitempty"`
	// DefaultFile is the file served when a directory is requested; defaults to index.html
	DefaultFile string `yaml:"default_file,omitempty"`
	// CacheControl is the Cache-Control response header value; when empty (the default), the
	// header is omitted and clients judge a file's freshness from its Last-Modified time
	CacheControl string `yaml:"cache_control,omitempty"`
	// CacheControlByExtension overrides CacheControl for files with the given extension (e.g., .js)
	CacheControlByExtension map[string]string `yaml:"cache_control_by_extension,omitempty"`
	// ResponseHeaders is a set of additional headers attached to every response
	ResponseHeaders map[string]string `yaml:"response_headers,omitempty"`
	// MIMETypes adds or overrides Content-Type values by file extension (e.g., .md)
	MIMETypes map[string]string `yaml:"mime_types,omitempty"`
	// NotFoundFile is a file, relative to Root, served in place of a plain 404 response
	NotFoundFile string `yaml:"not_found_file,omitempty"`
	// NotFoundStatus is the status NotFoundFile is served with: 404 (the default) for an
	// error page, or 200 for a single-page application that routes on the client
	NotFoundStatus int `yaml:"not_found_status,omitempty"`
	// DirectoryListing, when true, lists a requested directory that has no DefaultFile
	DirectoryListing bool `yaml:"directory_listing,omitempty"`
	// FileserverCache configures the Fileserver cache of small files held in memory. It is
	// named for what it is rather than for its key, as a backend has other caches.
	FileserverCache *FileserverCacheOptions `yaml:"cache,omitempty"`
}

// FileserverCacheOptions configures the Fileserver cache: small files held in memory
type FileserverCacheOptions struct {
	// Disabled, when true, serves every request from disk
	Disabled bool `yaml:"disabled,omitempty"`
	// MaxFileSizeBytes is the largest file held in memory; larger files stream from disk
	MaxFileSizeBytes int64 `yaml:"max_file_size_bytes,omitempty"`
	// MaxSizeBytes caps the memory used by held files, counting a fixed overhead
	// for each; files beyond it stream from disk
	MaxSizeBytes int64 `yaml:"max_size_bytes,omitempty"`
	// MaxFiles caps the number of files held, and with it the directories watched
	MaxFiles int64 `yaml:"max_files,omitempty"`
	// RevalidationInterval is how often held files are compared to disk, as a
	// backstop to filesystem change events
	RevalidationInterval timeconv.Duration `yaml:"revalidation_interval,omitempty"`
}

// errors returned by Validate
var (
	ErrMissingRoot          = errors.New("static.root is required")
	ErrInvalidDefaultFile   = errors.New("static.default_file must be a file name with no path and no leading '.'")
	ErrInvalidNotFoundFile  = errors.New("static.not_found_file must be a path within the root with no '.' segments")
	ErrInvalidNotFoundCode  = errors.New("static.not_found_status must be 404 or 200, and requires not_found_file")
	ErrInvalidMaxFileSize   = errors.New("static.cache.max_file_size_bytes must be greater than zero")
	ErrInvalidMaxSize       = errors.New("static.cache.max_size_bytes must not be less than max_file_size_bytes")
	ErrInvalidMaxFiles      = errors.New("static.cache.max_files must be greater than zero")
	ErrInvalidRevalInterval = errors.New("static.cache.revalidation_interval must be greater than zero")
)

// New returns the default static file server options
func New() *Options {
	return &Options{
		DefaultFile:     DefaultDefaultFile,
		FileserverCache: NewFileserverCache(),
	}
}

// NewFileserverCache returns the default fileserver cache options
func NewFileserverCache() *FileserverCacheOptions {
	return &FileserverCacheOptions{
		MaxFileSizeBytes:     DefaultMaxFileSizeBytes,
		MaxSizeBytes:         DefaultMaxSizeBytes,
		MaxFiles:             DefaultMaxFiles,
		RevalidationInterval: timeconv.Duration(DefaultRevalidationInterval),
	}
}

// Clone returns an independent copy
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.CacheControlByExtension = maps.Clone(o.CacheControlByExtension)
	out.ResponseHeaders = maps.Clone(o.ResponseHeaders)
	out.MIMETypes = maps.Clone(o.MIMETypes)
	out.FileserverCache = o.FileserverCache.Clone()
	return &out
}

// Clone returns an independent copy
func (o *FileserverCacheOptions) Clone() *FileserverCacheOptions {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Initialize normalizes the options: the root becomes an absolute path and
// extension keys are lowercased with a leading dot
func (o *Options) Initialize() error {
	if o == nil {
		return nil
	}
	if o.DefaultFile == "" {
		o.DefaultFile = DefaultDefaultFile
	}
	if o.FileserverCache == nil {
		o.FileserverCache = NewFileserverCache()
	}
	if o.Root != "" {
		root, err := filepath.Abs(o.Root)
		if err != nil {
			return fmt.Errorf("invalid static.root %q: %w", o.Root, err)
		}
		o.Root = root
	}
	o.CacheControlByExtension = normalizeExtensions(o.CacheControlByExtension)
	o.MIMETypes = normalizeExtensions(o.MIMETypes)
	if o.NotFoundFile != "" {
		o.NotFoundFile = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(o.NotFoundFile)), "/")
		if o.NotFoundStatus == 0 {
			o.NotFoundStatus = http.StatusNotFound
		}
	}
	return nil
}

// NormalizeExtension lowercases an extension and ensures its leading dot
func NormalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

func normalizeExtensions(in map[string]string) map[string]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[NormalizeExtension(k)] = v
	}
	return out
}

// Validate returns an error when the options cannot produce a working file server
func (o *Options) Validate() error {
	if o == nil || strings.TrimSpace(o.Root) == "" {
		return ErrMissingRoot
	}
	fi, err := os.Stat(o.Root)
	if err != nil {
		return fmt.Errorf("invalid static.root %q: %w", o.Root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("invalid static.root %q: not a directory", o.Root)
	}
	if o.DefaultFile == "" || strings.HasPrefix(o.DefaultFile, ".") ||
		strings.ContainsAny(o.DefaultFile, `/\`) {
		return ErrInvalidDefaultFile
	}
	if o.NotFoundFile == "" && o.NotFoundStatus != 0 {
		return ErrInvalidNotFoundCode
	}
	if o.NotFoundFile != "" {
		if o.NotFoundStatus != http.StatusNotFound && o.NotFoundStatus != http.StatusOK {
			return ErrInvalidNotFoundCode
		}
		if strings.HasPrefix(o.NotFoundFile, ".") || strings.Contains(o.NotFoundFile, "/.") ||
			strings.ContainsAny(o.NotFoundFile, "\\\x00") || strings.HasSuffix(o.NotFoundFile, "/") {
			return ErrInvalidNotFoundFile
		}
	}
	for ext, ct := range o.MIMETypes {
		if ext == "" || ext == "." {
			return fmt.Errorf("invalid static.mime_types extension %q", ext)
		}
		if _, _, err := mime.ParseMediaType(ct); err != nil {
			return fmt.Errorf("invalid static.mime_types value for %q: %w", ext, err)
		}
	}
	for ext := range o.CacheControlByExtension {
		if ext == "" || ext == "." {
			return fmt.Errorf("invalid static.cache_control_by_extension extension %q", ext)
		}
	}
	for name := range o.ResponseHeaders {
		if name == "" || http.CanonicalHeaderKey(name) == "" || strings.ContainsAny(name, " :\r\n") {
			return fmt.Errorf("invalid static.response_headers name %q", name)
		}
	}
	return o.FileserverCache.Validate()
}

// Validate returns an error when the fileserver cache options are unusable
func (o *FileserverCacheOptions) Validate() error {
	if o == nil || o.Disabled {
		return nil
	}
	if o.MaxFileSizeBytes <= 0 {
		return ErrInvalidMaxFileSize
	}
	if o.MaxSizeBytes < o.MaxFileSizeBytes {
		return ErrInvalidMaxSize
	}
	if o.MaxFiles <= 0 {
		return ErrInvalidMaxFiles
	}
	if o.RevalidationInterval <= 0 {
		return ErrInvalidRevalInterval
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Options
	value := plain(*New())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = Options(value)
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults
func (o *FileserverCacheOptions) UnmarshalYAML(unmarshal func(any) error) error {
	type plain FileserverCacheOptions
	value := plain(*NewFileserverCache())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = FileserverCacheOptions(value)
	return nil
}
