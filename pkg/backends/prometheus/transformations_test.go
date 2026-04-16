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

package prometheus

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

var testLogger = logging.NoopLogger()

func TestProcessTransformations(t *testing.T) {
	// passing test case is no panics
	c := &Client{injectLabels: map[string]string{"test": "trickster"}}
	c.ProcessTransformations(nil)
	c.hasTransformations = true
	c.ProcessTransformations(nil)
	c.ProcessTransformations(&dataset.DataSet{})
}

func TestProcessLabelsResponse_InjectsKeysAndValues(t *testing.T) {
	parse := func(body []byte) []string {
		t.Helper()
		var env struct {
			Status string   `json:"status"`
			Data   []string `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("unmarshal: %v (body=%s)", err, body)
		}
		if env.Status != "success" {
			t.Fatalf("want success, got %q", env.Status)
		}
		return env.Data
	}

	t.Run("labels endpoint appends injected keys", func(t *testing.T) {
		c := &Client{
			injectLabels:       map[string]string{"shard": "s1", "region": "us"},
			hasTransformations: true,
		}
		in := []byte(`{"status":"success","data":["__name__","job","instance"]}`)
		out := c.processLabelsResponse(in, "/api/v1/labels")
		got := parse(out)
		if !slices.Contains(got, "shard") || !slices.Contains(got, "region") {
			t.Fatalf("want injected keys in data, got %+v", got)
		}
	})

	t.Run("label values endpoint appends injected value when name matches", func(t *testing.T) {
		c := &Client{
			injectLabels:       map[string]string{"shard": "s1"},
			hasTransformations: true,
		}
		in := []byte(`{"status":"success","data":["prometheus"]}`)
		out := c.processLabelsResponse(in, "/api/v1/label/shard/values")
		got := parse(out)
		if !slices.Contains(got, "s1") {
			t.Fatalf("want injected value in data, got %+v", got)
		}
	})

	t.Run("label values endpoint does not inject when name does not match", func(t *testing.T) {
		c := &Client{
			injectLabels:       map[string]string{"shard": "s1"},
			hasTransformations: true,
		}
		in := []byte(`{"status":"success","data":["a","b"]}`)
		out := c.processLabelsResponse(in, "/api/v1/label/region/values")
		got := parse(out)
		if slices.Contains(got, "s1") {
			t.Fatalf("must not inject shard value into unrelated label endpoint, got %+v", got)
		}
	})
}

func TestDefaultWrite(t *testing.T) {
	w := httptest.NewRecorder()
	defaultWrite(200, w, []byte("trickster"))
	if w.Body.String() != "trickster" || w.Code != 200 {
		t.Error("write mismatch")
	}
}

func TestProcessVectorTransformations(t *testing.T) {
	logger.SetLogger(testLogger)
	c := &Client{}
	w := httptest.NewRecorder()

	rsc := &request.Resources{}
	body := []byte("trickster")
	statusCode := 200
	c.processVectorTransformations(w, body, statusCode, rsc)
	if w.Code != 200 {
		t.Errorf("expected %d got %d", 200, w.Code)
	}
}

