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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sizeconv"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"go.yaml.in/yaml/v3"
)

func TestIsEnabled(t *testing.T) {
	var nilOpts *Options
	if nilOpts.IsEnabled() {
		t.Error("expected nil options to be disabled")
	}
	if New().IsEnabled() {
		t.Error("expected empty options to be disabled")
	}
	if !(&Options{Filename: "/tmp/a.log"}).IsEnabled() {
		t.Error("expected access-only options to be enabled")
	}
	if !(&Options{ErrorFilename: "/tmp/e.log"}).IsEnabled() {
		t.Error("expected error-only options to be enabled")
	}
}

func TestValidate(t *testing.T) {
	o := New()
	if err := o.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if ok, err := o.Validate(); !ok || err != nil {
		t.Errorf("expected default options to validate: %v", err)
	}
	o.Format = "%h %Z"
	if _, err := o.Validate(); !errors.Is(err, format.ErrInvalidFormatToken) {
		t.Errorf("expected format error, got %v", err)
	}
	o.Format = format.Common
	o.ErrorFormat = "%Z"
	if _, err := o.Validate(); !errors.Is(err, format.ErrInvalidFormatToken) {
		t.Errorf("expected error format error, got %v", err)
	}
	o.ErrorFormat = ""
	o.ErrorThreshold = 42
	if _, err := o.Validate(); !errors.Is(err, ErrInvalidErrorThreshold) {
		t.Errorf("expected threshold error, got %v", err)
	}
	o.ErrorThreshold = 500
	if ok, err := o.Validate(); !ok || err != nil {
		t.Errorf("expected options to validate: %v", err)
	}
}

func TestResolvedInheritance(t *testing.T) {
	o := New()
	if o.ResolvedFormat() != format.DefaultFormatName {
		t.Errorf("unexpected default format: %s", o.ResolvedFormat())
	}
	if o.ResolvedErrorFormat() != format.DefaultFormatName {
		t.Errorf("unexpected default error format: %s", o.ResolvedErrorFormat())
	}
	if o.ResolvedErrorThreshold() != DefaultErrorThreshold {
		t.Errorf("unexpected default threshold: %d", o.ResolvedErrorThreshold())
	}
	o.Format = format.JSON
	if o.ResolvedErrorFormat() != format.JSON {
		t.Error("expected error format to inherit format")
	}
	o.ErrorFormat = format.Common
	o.ErrorThreshold = 500
	if o.ResolvedErrorFormat() != format.Common ||
		o.ResolvedErrorThreshold() != 500 {
		t.Error("expected explicit error format and threshold")
	}
}

func TestManagerOptionsInheritance(t *testing.T) {
	size := sizeconv.Size(64)
	count := 3
	compress := false
	o := &Options{
		Filename:      "/tmp/a.log",
		ErrorFilename: "/tmp/e.log",
		Rotation:      &manager.RotationOptions{Size: &size},
		Retention:     &manager.RetentionOptions{Count: &count},
		Compress:      &compress,
	}
	am := o.AccessManagerOptions()
	if am.Filename != "/tmp/a.log" || am.MaxSizeBytes != 64 ||
		am.RetentionCount != 3 || am.Compress {
		t.Errorf("unexpected access manager options: %+v", am)
	}
	// error log inherits the access log's rotation/retention/compression
	em := o.ErrorManagerOptions()
	if em.Filename != "/tmp/e.log" || em.MaxSizeBytes != 64 ||
		em.RetentionCount != 3 || em.Compress {
		t.Errorf("unexpected inherited error manager options: %+v", em)
	}
	// explicit error_* fields override
	errSize := sizeconv.Size(128)
	errCount := 7
	errCompress := true
	o.ErrorRotation = &manager.RotationOptions{Size: &errSize}
	o.ErrorRetention = &manager.RetentionOptions{Count: &errCount}
	o.ErrorCompress = &errCompress
	em = o.ErrorManagerOptions()
	if em.MaxSizeBytes != 128 || em.RetentionCount != 7 || !em.Compress {
		t.Errorf("unexpected explicit error manager options: %+v", em)
	}
}

func TestClone(t *testing.T) {
	var nilOpts *Options
	if nilOpts.Clone() != nil {
		t.Error("expected nil clone")
	}
	size := sizeconv.Size(64)
	age := timeconv.Duration(time.Hour)
	compress := false
	o := &Options{
		Filename:  "/tmp/a.log",
		Rotation:  &manager.RotationOptions{Size: &size},
		Retention: &manager.RetentionOptions{Age: &age},
		Compress:  &compress,
	}
	c := o.Clone()
	if c == o || c.Rotation == o.Rotation || c.Retention == o.Retention ||
		c.Compress == o.Compress {
		t.Error("expected deep clone")
	}
	if c.Filename != o.Filename || *c.Rotation.Size != 64 ||
		*c.Retention.Age != age || *c.Compress {
		t.Errorf("unexpected clone values: %+v", c)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	doc := `
filename: /var/log/trickster/example1.access.log
format: combined
rotation:
  size: 256MB
  interval: 1d
retention:
  count: 3
  age: 7d
compress: true
error_filename: /var/log/trickster/example1.error.log
error_threshold: 500
error_retention:
  count: 7
`
	o := New()
	if err := yaml.Unmarshal([]byte(doc), o); err != nil {
		t.Fatal(err)
	}
	if o.Filename != "/var/log/trickster/example1.access.log" ||
		o.Format != "combined" ||
		*o.Rotation.Size != 256*1024*1024 ||
		time.Duration(*o.Rotation.Interval) != 24*time.Hour ||
		*o.Retention.Count != 3 ||
		time.Duration(*o.Retention.Age) != 7*24*time.Hour ||
		!*o.Compress ||
		o.ErrorFilename != "/var/log/trickster/example1.error.log" ||
		o.ErrorThreshold != 500 ||
		*o.ErrorRetention.Count != 7 {
		t.Errorf("unexpected unmarshaled options: %+v", o)
	}
	if ok, err := o.Validate(); !ok || err != nil {
		t.Errorf("expected round-tripped options to validate: %v", err)
	}
	b, err := yaml.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	o2 := New()
	if err = yaml.Unmarshal(b, o2); err != nil {
		t.Fatal(err)
	}
	if *o2.Rotation.Size != *o.Rotation.Size ||
		*o2.Retention.Count != *o.Retention.Count {
		t.Error("expected marshal/unmarshal round trip to preserve values")
	}
}
