/*
 * Copyright 2026 The Trickster Authors
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
package lbtest

import "testing"

// reporter is the part of testing.TB the suite's checks use; both *testing.T and *testing.B
// have it, and so does the recorder the suite's own tests drive it with
type reporter interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// suiteT is a reporter that can run named checks. It stands between the suite and
// *testing.T so that the suite can be run against a strategy that is expected to fail it.
type suiteT interface {
	reporter
	Run(name string, check func(suiteT)) bool
}

// realT adapts *testing.T, whose Run hands its function a *testing.T
type realT struct{ *testing.T }

func (t realT) Run(name string, check func(suiteT)) bool {
	return t.T.Run(name, func(t *testing.T) { check(realT{t}) })
}
