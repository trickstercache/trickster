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

package tracing

import (
	"net/http"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func TestHTTPToCode(t *testing.T) {

	tests := []struct {
		code     int
		expected codes.Code
	}{
		{
			http.StatusMovedPermanently, codes.Ok,
		},
		{
			http.StatusNotFound, codes.Error,
		},
		{
			http.StatusBadRequest, codes.Error,
		},
		{
			http.StatusServiceUnavailable, codes.Error,
		},
		{
			http.StatusInternalServerError, codes.Error,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			v := HTTPToCode(test.code)
			if v != test.expected {
				t.Errorf("expected %d got %d", test.expected, v)
			}
		})
	}

}

func TestTags(t *testing.T) {

	t1 := Tags{"testKey1": "testValue1"}
	t2 := Tags{"testKey2": "testValue2"}

	t1.Merge(nil)
	if len(t1) != 1 {
		t.Errorf("expected %d got %d", 1, len(t1))
	}

	t1.Merge(t2)
	if len(t1) != 2 {
		t.Errorf("expected %d got %d", 2, len(t1))
	}

	t1.MergeAttr(nil)
	if len(t1) != 2 {
		t.Errorf("expected %d got %d", 2, len(t1))
	}

	attrs := []attribute.KeyValue{attribute.String("testKey3", "testValue3")}
	t1.MergeAttr(attrs)
	if len(t1) != 3 {
		t.Errorf("expected %d got %d", 3, len(t1))
	}

	attrs = t1.ToAttr()
	if len(attrs) != 3 {
		t.Errorf("expected %d got %d", 3, len(attrs))
	}

}
