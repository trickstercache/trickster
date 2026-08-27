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

package manager

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sizeconv"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func TestResolveOptionsDefaults(t *testing.T) {
	o := ResolveOptions("/tmp/x.log", nil, nil, nil)
	if o.Filename != "/tmp/x.log" ||
		o.MaxSizeBytes != DefaultMaxSizeBytes ||
		o.Interval != 0 ||
		o.RetentionCount != DefaultRetentionCount ||
		o.RetentionAge != DefaultRetentionAge ||
		!o.Compress {
		t.Errorf("unexpected resolved options: %+v", o)
	}
}

func TestResolveOptionsExplicit(t *testing.T) {
	size := sizeconv.Size(64)
	interval := timeconv.Duration(time.Hour)
	count := 0
	age := timeconv.Duration(0)
	compress := false
	o := ResolveOptions("/tmp/x.log",
		&RotationOptions{Size: &size, Interval: &interval},
		&RetentionOptions{Count: &count, Age: &age}, &compress)
	if o.MaxSizeBytes != 64 || o.Interval != time.Hour ||
		o.RetentionCount != 0 || o.RetentionAge != 0 || o.Compress {
		t.Errorf("unexpected resolved options: %+v", o)
	}
}

func TestRotationRetentionClone(t *testing.T) {
	var nilRot *RotationOptions
	var nilRet *RetentionOptions
	if nilRot.Clone() != nil || nilRet.Clone() != nil {
		t.Error("expected nil clones")
	}
	size := sizeconv.Size(64)
	count := 3
	rot := &RotationOptions{Size: &size}
	ret := &RetentionOptions{Count: &count}
	rotC, retC := rot.Clone(), ret.Clone()
	if rotC.Size == rot.Size || *rotC.Size != 64 {
		t.Error("expected deep rotation clone")
	}
	if retC.Count == ret.Count || *retC.Count != 3 {
		t.Error("expected deep retention clone")
	}
	if rotC.Interval != nil || retC.Age != nil {
		t.Error("expected nil unset fields")
	}
}
