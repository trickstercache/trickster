/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package reverseproxycache

import (
	"testing"

	"github.com/Comcast/trickster/internal/config"
)

func TestFastForwardURL(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	n1, n2 := c.FastForwardURL(nil)
	if n1 != nil && n2 != nil {
		t.Errorf("expected nil return for stub functions in %s", "ReverseProxyCache")
	}
}

func TestUnmarshalInstantaneous(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	n1, n2 := c.UnmarshalInstantaneous(nil)
	if n1 != nil && n2 != nil {
		t.Errorf("expected nil return for stub functions in %s", "ReverseProxyCache")
	}
}

func TestMarshalTimeseries(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	n1, n2 := c.MarshalTimeseries(nil)
	if n1 != nil && n2 != nil {
		t.Errorf("expected nil return for stub functions in %s", "ReverseProxyCache")
	}
}

func TestUnmarshalTimeseries(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	n1, n2 := c.UnmarshalTimeseries(nil)
	if n1 != nil && n2 != nil {
		t.Errorf("expected nil return for stub functions in %s", "ReverseProxyCache")
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	n1, n2 := c.ParseTimeRangeQuery(nil)
	if n1 != nil && n2 != nil {
		t.Errorf("expected nil return for stub functions in %s", "ReverseProxyCache")
	}
}

func TestSetExtent(t *testing.T) {
	c := NewClient("test", config.NewOriginConfig(), nil)
	c.SetExtent(nil, nil)
}
