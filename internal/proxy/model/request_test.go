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

package model

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestNewRequest(t *testing.T) {
	url := &url.URL{}

	cfg := config.NewOriginConfig()
	cfg.Name = "test"
	cfg.OriginType = "testType"

	headers := make(http.Header)
	r := NewRequest(cfg, "testhandler", http.MethodGet, url, headers, time.Duration(1)*time.Second, nil, tu.NewTestWebClient())
	if r.OriginConfig.OriginType != "testType" {
		t.Errorf("expected 'testType' got '%s'", r.OriginConfig.OriginType)
	}
}

func TestCopy(t *testing.T) {
	cfg := config.NewOriginConfig()
	cfg.Name = "test"
	cfg.OriginType = "testType"
	url := &url.URL{}
	headers := make(http.Header)
	r := NewRequest(cfg, "testhandler", http.MethodGet, url, headers, time.Duration(1)*time.Second, nil, tu.NewTestWebClient())
	r2 := r.Copy()
	if r2.OriginConfig.OriginType != "testType" {
		t.Errorf("expected 'testType' got '%s'", r2.OriginConfig.OriginType)
	}
}
