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

// Package manager provides rotating, retention-managed log file writers.
package manager

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DefaultMaxSizeBytes is the default rotation size threshold (256 MB)
	DefaultMaxSizeBytes = int64(256 * 1024 * 1024)
	// DefaultRetentionCount is the default maximum number of archived files
	DefaultRetentionCount = 80
	// DefaultRetentionAge is the default maximum age of archived files
	DefaultRetentionAge = 7 * 24 * time.Hour
	// DefaultCompress indicates whether archived files are gzipped by default
	DefaultCompress = true
	// DefaultFileMode is the default mode for created log files
	DefaultFileMode = fs.FileMode(0o644)
	// DefaultDirMode is the default mode for created log directories
	DefaultDirMode = fs.FileMode(0o755)
)

// ErrNoFilename is returned when a Writer is requested without a Filename
var ErrNoFilename = errors.New("log filename is required")

// ErrConflictingOptions is returned when one file has incompatible managers
var ErrConflictingOptions = errors.New("conflicting log manager options")

// ErrBufferFull is returned when a log line exceeds the bounded write buffer.
var ErrBufferFull = errors.New("log write buffer is full")

// Options configures a managed log file
type Options struct {
	// Filename is the path to the live log file
	Filename string
	// MaxSizeBytes rotates the file when a write would exceed this size (0 disables)
	MaxSizeBytes int64
	// Interval rotates the file when it has been open at least this long (0 disables)
	Interval time.Duration
	// RetentionCount is the max archived files kept (0 disables count pruning)
	RetentionCount int
	// RetentionAge prunes archived files older than this (0 disables age pruning)
	RetentionAge time.Duration
	// Compress indicates whether archived files are gzipped
	Compress bool
	// FileMode is the mode used when creating log files
	FileMode fs.FileMode
}

// NewOptions returns Options with default rotation and retention values
func NewOptions() *Options {
	return &Options{
		MaxSizeBytes:   DefaultMaxSizeBytes,
		RetentionCount: DefaultRetentionCount,
		RetentionAge:   DefaultRetentionAge,
		Compress:       DefaultCompress,
		FileMode:       DefaultFileMode,
	}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	out := *o
	return &out
}

func normalizeOptions(o *Options) (Options, error) {
	if o == nil || o.Filename == "" {
		return Options{}, ErrNoFilename
	}
	out := *o
	name, err := filepath.Abs(out.Filename)
	if err != nil {
		return Options{}, err
	}
	out.Filename = name
	if out.FileMode == 0 {
		out.FileMode = DefaultFileMode
	}
	return out, nil
}

func optionsEqual(a, b Options) bool {
	return a == b
}

// ValidateOptions rejects incompatible manager settings for the same file.
func ValidateOptions(options ...*Options) error {
	seen := make(map[string]Options, len(options))
	for _, o := range options {
		if o == nil || o.Filename == "" {
			continue
		}
		resolved, err := normalizeOptions(o)
		if err != nil {
			return err
		}
		if prior, ok := seen[resolved.Filename]; ok &&
			!optionsEqual(prior, resolved) {
			return fmt.Errorf("%w for %s", ErrConflictingOptions,
				resolved.Filename)
		}
		seen[resolved.Filename] = resolved
	}
	return nil
}

// InstanceFilename returns the filename adjusted to include the provided
// instance ID (e.g., trickster.log -> trickster.1.log) when id is positive
func InstanceFilename(name string, id int) string {
	if id <= 0 {
		return name
	}
	ext := filepath.Ext(filepath.Base(name))
	stem := name[:len(name)-len(ext)]
	return stem + "." + strconv.Itoa(id) + ext
}
