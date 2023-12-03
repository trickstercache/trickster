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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()

	startSecs := fmt.Sprintf("%d", start.Unix())
	endSecs := fmt.Sprintf("%d", end.Unix())

	expected := "end=" + endSecs + "&q=up&start=" + startSecs

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090", "-provider", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	pc := client.(*Client)

	u := &url.URL{RawQuery: "q=up"}

	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	e := &timeseries.Extent{Start: start, End: end}
	pc.SetExtent(r, nil, e)

	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, r.URL.RawQuery)
	}

	u2 := urls.Clone(u)
	u2.RawQuery = ""

	b := bytes.NewBufferString(expected)
	r, _ = http.NewRequest(http.MethodPost, u2.String(), b)

	pc.SetExtent(r, nil, e)
	if int(r.ContentLength) != len(expected) {
		b, _ := io.ReadAll(r.Body)
		fmt.Println(string(b))
		t.Errorf("expected %d got %d", len(expected), r.ContentLength)
	}

}

func TestFastForwardURL(t *testing.T) {

	expected := "q=up"

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090", "-provider", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	pc := client.(*Client)

	u := &url.URL{Path: "/query_range", RawQuery: "q=up&start=1&end=1&step=1"}
	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)

	r2, err := pc.FastForwardRequest(r)
	if err != nil {
		t.Error(err)
	}

	if expected != r2.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, r2.URL.RawQuery)
	}

	r2.URL.RawQuery = ""
	b := bytes.NewBufferString(expected)
	r, _ = http.NewRequest(http.MethodPost, r2.URL.String(), b)

	_, err = pc.FastForwardRequest(r)
	if err != nil {
		t.Error(err)
	}

}
