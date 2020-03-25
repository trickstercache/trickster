/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package reverseproxycache

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/config"
)

func TestBuildUpstreamURL(t *testing.T) {

	expected := "q=up&start=1&end=1&step=1"

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "none:9090", "-origin-type", "rpc", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := Client{config: oc, name: "default"}

	u := &url.URL{Path: "/default/query_range", RawQuery: expected}

	r, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		t.Error(err)
	}

	u2 := client.BuildUpstreamURL(r)

	if expected != u2.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, u2.RawQuery)
	}

	u = &url.URL{Path: "/default//", RawQuery: ""}

	r, err = http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		t.Error(err)
	}

	u2 = client.BuildUpstreamURL(r)

	if u2.Path != "/" {
		t.Errorf("\nexpected [%s]\ngot [%s]", "/", u2.Path)
	}

}
