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

package options

import (
	"testing"
	"time"

	ro "github.com/tricksterproxy/trickster/pkg/proxy/origins/rule/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
)

func TestNewOptions(t *testing.T) {
	o := NewOptions()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestClone(t *testing.T) {
	p := po.NewOptions()
	o := NewOptions()
	o.Hosts = []string{"test"}
	o.CacheName = "test"
	o.CompressableTypes = map[string]bool{"test": true}
	o.HealthCheckHeaders = map[string]string{"test": "test"}
	o.Paths = map[string]*po.Options{"test": p}
	o.NegativeCache = map[int]time.Duration{1: 1}
	o.FastForwardPath = p
	o.RuleOptions = &ro.Options{}
	o2 := o.Clone()
	if o2.CacheName != "test" {
		t.Error("clone failed")
	}

}

func TestValidateOriginName(t *testing.T) {

	err := ValidateOriginName("test")
	if err != nil {
		t.Error(err)
	}

	err = ValidateOriginName("frontend")
	if err == nil {
		t.Error("expected error for invalid origin name")
	}

}
