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

import "testing"

func testResult() *Result {
	r := &Result{
		StatementID: 42,
		SeriesList:  []*Series{testSeries()},
	}
	return r
}

func TestResultHashes(t *testing.T) {
	r := &Result{}
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
	r.SeriesList = append(r.SeriesList, nil)
	r2 := r.Clone()
	if r2.StatementID != r.StatementID {
		t.Error("result clone mismatch")
	}
	if r2.SeriesList[0].Header.Name != r.SeriesList[0].Header.Name {
		t.Error("result clone mismatch")
	}
}

func TestResultSize(t *testing.T) {
	const expected = 116
	i := testResult().Size()
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}

func TestResultString(t *testing.T) {
	const expected = `{"error":"test_error","statementID":42,` +
		`series:[{"header":{"name":"test",` +
		`"query":"SELECT TRICKSTER!","tags":"test1=value1",` +
		`"fields":["Field1"],"timestampIndex":37},` +
		`points:[{5000000000,1,37},{10000000000,1,24}]},` +
		`{"header":{"name":"test2",` +
		`"query":"SELECT TRICKSTER!","tags":"test1=value1",` +
		`"fields":["Field1"],"timestampIndex":37}` +
		`,points:[{5000000000,1,37},{10000000000,1,24}]}]}`
	r := testResult()
	r.Error = "test_error"
	r.SeriesList = append(r.SeriesList, testSeries())
	r.SeriesList[1].Header.Name = "test2"
	s := r.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}
