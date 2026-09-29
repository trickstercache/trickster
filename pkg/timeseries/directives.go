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

package timeseries

import "time"

// Directives holds the trickster-* directives a client wrote into a query's comments
type Directives struct {
	// VolatileWindow is the query's volatile window, when above zero
	VolatileWindow time.Duration
	// StepAlignment is the query's step alignment mode, when set
	StepAlignment StepAlignment
	// FastForwardDisable is true when the query turns Fast Forward off
	FastForwardDisable bool
}

// IsZero reports whether no directive is set
func (d Directives) IsZero() bool {
	return d == Directives{}
}
