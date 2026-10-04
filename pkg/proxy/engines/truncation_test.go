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

package engines

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestProxyOnlyWarned(t *testing.T) {
	const prefix = "member unavailable"
	warned := &dataset.DataSet{Warnings: []string{"other", prefix + ": east"}}
	clean := &dataset.DataSet{Warnings: []string{"other"}}
	if proxyOnlyWarned("", warned) {
		t.Fatal("an empty prefix flags nothing")
	}
	if !proxyOnlyWarned(prefix, clean, nil, warned) {
		t.Fatal("a warning with the prefix flags the result")
	}
	if proxyOnlyWarned(prefix, clean, (*dataset.DataSet)(nil)) {
		t.Fatal("results without the warning are not flagged")
	}
}
