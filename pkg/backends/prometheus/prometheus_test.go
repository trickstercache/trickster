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
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registration"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var testModeler = model.NewModeler()

func TestPrometheusClientInterfacing(t *testing.T) {

	// this test ensures the client will properly conform to the
	// Backend and TimeseriesBackend interfaces

	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	var oc backends.Backend = c
	var tc backends.TimeseriesBackend = c.(*Client)

	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}

	if tc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", tc.Name())
	}
}

func TestNewClient(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	o := &bo.Options{Provider: "TEST_CLIENT"}
	c, err := NewClient("default", o, nil, cache, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Provider)
	}

	if c.Configuration().Provider != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Provider)
	}
}

func TestParseTime(t *testing.T) {
	fixtures := []struct {
		input  string
		output string
	}{
		{"2018-04-07T05:08:53.200Z", "2018-04-07 05:08:53.2 +0000 UTC"},
		{"1523077733", "2018-04-07 05:08:53 +0000 UTC"},
		{"1523077733.2", "2018-04-07 05:08:53.2 +0000 UTC"},
	}

	for _, f := range fixtures {
		out, err := parseTime(f.input)
		if err != nil {
			t.Error(err)
		}

		outStr := out.UTC().String()
		if outStr != f.output {
			t.Errorf("Expected %s, got %s for input %s", f.output, outStr, f.input)
		}
	}
}

func TestParseTimeFails(t *testing.T) {
	_, err := parseTime("a")
	if err == nil {
		t.Errorf(`expected error 'cannot parse "a" to a valid timestamp'`)
	}
}

func TestParseTimeRangeQuery(t *testing.T) {

	qp := url.Values(map[string][]string{
		"query": {`up-` + timeseries.FastForwardUserDisableFlag + " " +
			timeseries.BackfillToleranceFlag + "30a"},
		"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
		"end":   {strconv.Itoa(int(time.Now().Unix()))},
		"step":  {"15"},
	})

	u := &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: qp.Encode(),
	}

	req := &http.Request{URL: u}
	o := bo.New()
	o.Prometheus = &po.Options{Labels: map[string]string{"test": "trickster"}}
	rsc := request.NewResources(o, nil, nil, nil, nil, nil, nil)
	req = request.SetResources(req, rsc)

	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if int(res.Step.Seconds()) != 15 {
			t.Errorf("expected 15 got %d", int(res.Step.Seconds()))
		}

		if int(res.Extent.End.Sub(res.Extent.Start).Hours()) != 6 {
			t.Errorf("expected 6 got %d", int(res.Extent.End.Sub(res.Extent.Start).Hours()))
		}
	}

	b := bytes.NewBufferString(qp.Encode())
	u.RawQuery = ""
	req, _ = http.NewRequest(http.MethodPost, u.String(), b)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	}
}

func TestParseTimeRangeQueryMissingQuery(t *testing.T) {
	expected := pe.MissingURLParam(upQuery).Error()
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query_": {`up`},
			"start":  {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":    {strconv.Itoa(int(time.Now().Unix()))},
			"step":   {"15"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeBadStartTime(t *testing.T) {
	const color = "red"
	expected := fmt.Errorf(`cannot parse "%s" to a valid timestamp`, color)
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {color},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"15"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected.Error() {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeBadEndTime(t *testing.T) {
	const color = "blue"
	expected := fmt.Errorf(`cannot parse "%s" to a valid timestamp`, color)
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {color},
			"step":  {"15"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected.Error() {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {

	expected := `duration literal x: expected value of at least length 2 at position 0`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"x"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoStart(t *testing.T) {

	expected := `missing URL parameter: [start]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"x"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoEnd(t *testing.T) {

	expected := `missing URL parameter: [end]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"step":  {"x"}}).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryNoStep(t *testing.T) {

	expected := `missing URL parameter: [step]`

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {strconv.Itoa(int(time.Now().Unix()))}},
		).Encode(),
	}}
	client := &Client{}
	_, _, _, err := client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryWithOffset(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up and has offset `},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"15"},
		}).Encode(),
	}}
	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
		return
	}

	if !res.IsOffset {
		t.Errorf("expected true got %t", res.IsOffset)
	}

}

func TestParseVectorQuery(t *testing.T) {

	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up and has offset `},
			"time":  {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
		}).Encode(),
	}}

	o := bo.New()
	o.Prometheus = &po.Options{Labels: map[string]string{"test": "trickster"}}
	rsc := request.NewResources(o, nil, nil, nil, nil, nil, nil)
	req = request.SetResources(req, rsc)

	rounder := time.Second * 15

	_, err := parseVectorQuery(req, rounder)
	if err != nil {
		t.Error(err)
	}

	req = &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"time": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
		}).Encode(),
	}}

	_, err = parseVectorQuery(req, rounder)
	if err == nil {
		t.Error("expected error for missing parameter")
	}

	req = &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up and has offset `},
			"time":  {`a`},
		}).Encode(),
	}}

	_, err = parseVectorQuery(req, rounder)
	if err == nil {
		t.Error("expected error for time parsing")
	}

	req = &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up and has offset `},
		}).Encode(),
	}}

	_, err = parseVectorQuery(req, rounder)
	if err != nil {
		t.Error(err)
	}

}
