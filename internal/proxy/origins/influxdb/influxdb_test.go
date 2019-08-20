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

package influxdb

import (
	"os"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/go-kit/kit/log"
)

var logger log.Logger

func init() {
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	metrics.Init(logger)
}
func TestNewClient(t *testing.T) {

	err := config.Load("trickster", "test", nil)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig(logger)
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	oc := &config.OriginConfig{Type: "TEST_CLIENT"}
	c := NewClient("default", oc, cache, logger)

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Type != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Type)
	}

	if c.Configuration().Type != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Type)
	}
}

func TestConfiguration(t *testing.T) {
	oc := &config.OriginConfig{Type: "TEST"}
	client := Client{config: oc, logger: logger}
	c := client.Configuration()
	if c.Type != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.Type)
	}
}

func TestCache(t *testing.T) {

	err := config.Load("trickster", "test", nil)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig(logger)
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	client := Client{cache: cache, logger: logger}
	c := client.Cache()

	if c.Configuration().Type != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Configuration().Type)
	}
}

func TestName(t *testing.T) {

	client := Client{name: "TEST", logger: logger}
	c := client.Name()

	if c != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c)
	}

}

func TestHTTPClient(t *testing.T) {
	oc := &config.OriginConfig{Type: "TEST"}

	client := NewClient("test", oc, nil, logger)

	if client.HTTPClient() == nil {
		t.Errorf("missing http client")
	}
}
