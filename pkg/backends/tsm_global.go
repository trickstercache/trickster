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

// TSMGlobalSource is a backend's raw series transport; the query language evaluating over it defines
// how series are read, so TSM only hands it to that language's evaluator.
type TSMGlobalSource interface {
	// SeriesAPI names the transport, such as remote_read
	SeriesAPI() string
}

// TSMGlobalSourceProvider is implemented by backends that serve their raw series to TSM global evaluation.
type TSMGlobalSourceProvider interface {
	TSMGlobalSource() (TSMGlobalSource, error)
}

// TSMGlobalMember is a pool member read by one global evaluation. Members are listed in configured
// order, which decides which replica's sample is kept when replicas disagree.
type TSMGlobalMember struct {
	Name   string
	Source TSMGlobalSource
}

// TSMGlobalUnavailable is a configured member one global evaluation cannot read; Reason is nil for a
// member the pool holds out of service.
type TSMGlobalUnavailable struct {
	Name   string
	Reason error
}

// TSMGlobalRequest lists what one global evaluation reads, and what it cannot.
type TSMGlobalRequest struct {
	Members     []TSMGlobalMember
	Unavailable []TSMGlobalUnavailable
}

// TSMGlobalEvaluator answers the requests it handles by evaluating each complete query once over the
// raw series of every member.
type TSMGlobalEvaluator interface {
	// Handles reports whether the evaluator answers r; other requests are merged from member responses
	Handles(r *http.Request) bool
	ServeTSMGlobal(w http.ResponseWriter, r *http.Request, req TSMGlobalRequest)
}

// TSMGlobalEvaluatorProvider is implemented by backends whose query language can evaluate globally.
type TSMGlobalEvaluatorProvider interface {
	NewTSMGlobalEvaluator(settings merge.TSMGlobalSettings) (TSMGlobalEvaluator, error)
}
