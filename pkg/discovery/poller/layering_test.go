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

package poller

import (
	"os/exec"
	"strings"
	"testing"
)

// The poller sits below both of its consumers: pkg/backends/healthcheck
// imports it, and so does every autodiscovery provider. If it ever reached
// back to either, the build would stop compiling -- so this pins the
// invariant with a clear failure message rather than leaving the next author
// to decode an import-cycle error.
//
// Note the constraint is narrower than "must not reach pkg/backends at all",
// which is not achievable for any package that logs: pkg/observability/logging
// imports pkg/config, which pulls in pkg/backends/options. That is pre-existing
// weight in the logging package, and it is harmless here because
// pkg/backends/options does not import pkg/backends/healthcheck.
func TestPollerDoesNotDependOnItsConsumers(t *testing.T) {
	forbidden := []string{
		"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck",
		"github.com/trickstercache/trickster/v2/pkg/discovery",
	}
	for _, pkg := range []string{
		"github.com/trickstercache/trickster/v2/pkg/discovery/poller",
		"github.com/trickstercache/trickster/v2/pkg/discovery/poller/http",
	} {
		t.Run(pkg, func(t *testing.T) {
			out, err := exec.Command("go", "list", "-deps", pkg).Output()
			if err != nil {
				t.Skipf("go list unavailable: %v", err)
			}
			deps := strings.SplitSeq(strings.TrimSpace(string(out)), "\n")
			for dep := range deps {
				for _, bad := range forbidden {
					if dep == bad {
						t.Errorf("%s depends on %s; the poller must stay below its consumers", pkg, bad)
					}
				}
			}
		})
	}
}
