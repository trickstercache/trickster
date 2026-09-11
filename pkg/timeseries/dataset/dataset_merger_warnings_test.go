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

package dataset

import (
	"slices"
	"testing"
)

func TestDefaultMergerPreservesWarningsAndStatus(t *testing.T) {
	t.Run("warnings from all shards are preserved", func(t *testing.T) {
		base := testDataSet()
		base.Warnings = []string{"base-warn"}
		base.Status = "success"

		s2 := testDataSet()
		s2.Warnings = []string{"shard2-warn-a", "shard2-warn-b"}
		s2.Status = "success"

		s3 := testDataSet()
		s3.Warnings = []string{"shard3-warn"}
		s3.Status = "success"

		base.Merge(false, s2, s3)

		for _, want := range []string{"base-warn", "shard2-warn-a", "shard2-warn-b", "shard3-warn"} {
			if !slices.Contains(base.Warnings, want) {
				t.Errorf("merged warnings missing %q; got %v", want, base.Warnings)
			}
		}
	})

	t.Run("error status is upgraded by a success shard", func(t *testing.T) {
		base := testDataSet()
		base.Status = "error"

		s2 := testDataSet()
		s2.Status = "success"

		base.Merge(false, s2)

		if base.Status != "success" {
			t.Errorf("status: want success after merging a success shard into error base, got %q", base.Status)
		}
	})

	t.Run("error status is preserved when all shards report error", func(t *testing.T) {
		base := testDataSet()
		base.Status = "error"

		s2 := testDataSet()
		s2.Status = "error"

		base.Merge(false, s2)

		if base.Status != "error" {
			t.Errorf("status: want error when all shards report error, got %q", base.Status)
		}
	})

	t.Run("empty base status takes on shard status", func(t *testing.T) {
		base := testDataSet()
		base.Status = ""

		s2 := testDataSet()
		s2.Status = "success"

		base.Merge(false, s2)

		if base.Status != "success" {
			t.Errorf("status: want success from shard when base empty, got %q", base.Status)
		}
	})

	t.Run("nil and empty shard warnings are no-ops", func(t *testing.T) {
		base := testDataSet()
		base.Warnings = []string{"keep-me"}

		s2 := testDataSet()
		// no warnings

		base.Merge(false, s2, nil)

		if !slices.Contains(base.Warnings, "keep-me") {
			t.Errorf("base warning lost; got %v", base.Warnings)
		}
	})
}
