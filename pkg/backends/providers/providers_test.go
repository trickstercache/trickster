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

package providers

import (
	"strconv"
	"testing"
)

func TestProviderString(t *testing.T) {

	t1 := RPC
	t2 := Prometheus
	var t3 Provider = 13

	if t1.String() != "rpc" {
		t.Errorf("expected %s got %s", "rpc", t1.String())
	}

	if t2.String() != "prometheus" {
		t.Errorf("expected %s got %s", "prometheus", t2.String())
	}

	if t3.String() != "13" {
		t.Errorf("expected %s got %s", "13", t3.String())
	}

}

func TestIsValidProvider(t *testing.T) {

	tests := []struct {
		o        string
		expected bool
	}{
		{"rpc", true},
		{"prometheus", true},
		{"", false},
		{"invalid", false},
		{"influxdb", true},
		{"irondb", true},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := IsValidProvider(test.o)
			if test.expected != res {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}

}

func TestIsSupportedTimeSeriesProvider(t *testing.T) {

	name := "test-should-fail"
	ok := IsSupportedTimeSeriesProvider(name)
	if ok {
		t.Error("expected false")
	}

	name = "prometheus"
	ok = IsSupportedTimeSeriesProvider(name)
	if !ok {
		t.Error("expected true")
	}

}
