/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

import "testing"

func testResult() Result {
	s := testSeries()
	r := Result{
		StatementID: 42,
		SeriesList:  []*Series{s},
	}
	return r
}

func TestResultHashes(t *testing.T) {
	r := Result{}
	if r.Hashes() != nil {
		t.Error("expected nil hashes list")
	}
	r = testResult()
	if len(r.Hashes()) != 1 {
		t.Errorf("expected %d got %d", 1, len(r.Hashes()))
	}
}

func TestResultClone(t *testing.T) {
	r := testResult()
	r2 := r.Clone()
	if r2.StatementID != r.StatementID {
		t.Error("result clone mismatch")
	}
	if r2.SeriesList[0].Header.Name != r.SeriesList[0].Header.Name {
		t.Error("result clone mismatch")
	}
}
