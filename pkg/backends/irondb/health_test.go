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

package irondb

import (
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func TestDefaultHealthCheckConfig(t *testing.T) {

	c, _ := NewClient("test", bo.New(), nil, nil, nil, nil)

	dho := c.DefaultHealthCheckConfig()
	if dho == nil {
		t.Error("expected non-nil result")
	}

	if dho.Path != "" {
		t.Error("expected / for path", dho.Path)
	}

}
