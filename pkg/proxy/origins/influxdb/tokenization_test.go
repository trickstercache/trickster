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

package influxdb

import (
	"testing"
	"time"
)

func TestGetQueryParts(t *testing.T) {

	s, ex := getQueryParts("")

	if s != "" {
		t.Errorf("expected empty string, got %s", s)
	}

	if ex.Start.Unix() != 0 {
		t.Errorf("expected 0, got %d", ex.Start.Unix())
	}

	s, ex = getQueryParts("where time >= 150000ms and time <= 150001ms")

	expected := "where <$TIME_TOKEN$>"
	if s != expected {
		t.Errorf("expected %s, got %s", expected, s)
	}

	if ex.Start.Unix() != 150 {
		t.Errorf("expected 150, got %d", ex.Start.Unix())
	}

}

func TestTokenizeQuery(t *testing.T) {

	out := tokenizeQuery("where time >= 150000ms and time <= 150001ms",
		map[string]string{"timeExpr1": "exp1", "timeExpr2": "exp2", "preOp2": "where", "postOp2": "and"})

	expected := "where time >= 150000ms and time <= 150001ms"
	if out != expected {
		t.Errorf("expected %s, got %s", expected, out)
	}

}

func TestTimeFromParts(t *testing.T) {

	tm := timeFromParts("1", map[string]string{"now1": "a", "offset1": "1s", "operand1": "+"})

	if tm.Before(time.Now().Add(time.Hour * -1)) {
		t.Errorf("expected recent time, got %d", tm.Unix())
	}

}
