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
)

func TestFastForwardURL(t *testing.T) {

	client := &Client{}
	r, err := client.FastForwardRequest(nil)
	if r != nil {
		t.Errorf("Expected nil url, got %v", r)
	}
	if err != nil {
		t.Errorf("Expected nil err, got %s", err)
	}
}

func TestUnmarshalInstantaneous(t *testing.T) {

	client := &Client{}
	tr, err := client.UnmarshalInstantaneous(nil)

	if tr != nil {
		t.Errorf("Expected nil timeseries, got %s", tr)
	}

	if err != nil {
		t.Errorf("Expected nil err, got %s", err)
	}

}
