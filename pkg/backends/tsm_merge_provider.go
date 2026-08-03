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

package backends

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

// TSMMergeProvider prepares a validated execution plan for every TSM request.
// Provider-specific query parsing, request cloning, and wire rewriting belong
// in plan construction so the executor remains independent of query syntax.
type TSMMergeProvider interface {
	PlanTSMMerge(r *http.Request, query string) (*merge.TSMMergePlan, error)
}

// TSMInjectedLabelProvider exposes provider-injected label keys that must be
// removed before TSM hashes and merges series. Virtual backends implement this
// by returning the union from their terminal time-series leaves.
type TSMInjectedLabelProvider interface {
	TSMInjectedLabelKeys() []string
}
