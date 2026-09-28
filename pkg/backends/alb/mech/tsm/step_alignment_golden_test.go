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

package tsm

import (
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
)

func TestMergeUnderMixedStepAlignments(t *testing.T) {
	// members answering on different grids interleave, so every merged point holds one member's value
	for _, test := range []struct{ name, peer string }{
		{"misaligned", "step_alignment/off_member"},
		{"aligned", "step_alignment/truncate_peer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			merged := loadGoldenDataSet(t, "step_alignment/truncate_member")
			merged.MergeWithStrategy(true, int(tsmerge.StrategySum), loadGoldenDataSet(t, test.peer))
			want := "step_alignment/" + test.name + "_sum"
			if *golden.Update && os.Getenv(regenGoldensEnv) == "1" {
				writeGoldenDataSet(t, want, merged)
			}
			require.Equal(t, toGolden(loadGoldenDataSet(t, want)), toGolden(merged))
		})
	}
}
