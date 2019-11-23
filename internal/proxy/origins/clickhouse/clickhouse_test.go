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

package clickhouse

import (
	"net/http"
	"net/url"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

func TestNewClient(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-origin-type", "clickhouse", "-origin-url", "http://1"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	oc := &config.OriginConfig{OriginType: "TEST_CLIENT"}
	c, err := NewClient("default", oc, cache)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().CacheType != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().CacheType)
	}

	if c.Configuration().OriginType != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().OriginType)
	}
}

func TestConfiguration(t *testing.T) {
	oc := &config.OriginConfig{OriginType: "TEST"}
	client := Client{config: oc}
	c := client.Configuration()
	if c.OriginType != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.OriginType)
	}
}

func TestCache(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-origin-type", "clickhouse", "-origin-url", "http://1"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	client := Client{cache: cache}
	c := client.Cache()

	if c.Configuration().CacheType != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Configuration().CacheType)
	}
}

func TestName(t *testing.T) {

	client := Client{name: "TEST"}
	c := client.Name()

	if c != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c)
	}

}

func TestHTTPClient(t *testing.T) {
	oc := &config.OriginConfig{OriginType: "TEST"}

	client, err := NewClient("test", oc, nil)
	if err != nil {
		t.Error(err)
	}

	if client.HTTPClient() == nil {
		t.Errorf("missing http client")
	}
}

func TestSetCache(t *testing.T) {
	c, err := NewClient("test", config.NewOriginConfig(), nil)
	if err != nil {
		t.Error(err)
	}
	c.SetCache(nil)
	if c.Cache() != nil {
		t.Errorf("expected nil cache for client named %s", "test")
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: testRawQuery(),
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err != nil {
		t.Error(err)
	} else {

		if res.Step.Seconds() != 60 {
			t.Errorf("expected 60 got %f", res.Step.Seconds())
		}

		if res.Extent.End.Sub(res.Extent.Start).Hours() != 6 {
			t.Errorf("expected 6 got %f", res.Extent.End.Sub(res.Extent.Start).Hours())
		}
	}

	req.URL.RawQuery = ""
	res, err = client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf("expected error for: %s", "missing URL parameter: [query]")
	}

	req.URL.RawQuery = "query=select+MISSING+STEP"
	res, err = client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf("expected error for: %s", "unable to parse timeseries step from downstream request")
	}

	req.URL.RawQuery = ""
	res, err = client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf("expected error for: %s", "missing URL parameter: [query]")
	}

}
