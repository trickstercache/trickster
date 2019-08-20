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

package prometheus

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/go-kit/kit/log"
)

var logger log.Logger

func init() {
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))
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

	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
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

func TestConfiguration(t *testing.T) {
	oc := &config.OriginConfig{Type: "TEST"}
	client := Client{config: oc, logger: logger}
	c := client.Configuration()
	if c.Type != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.Type)
	}
}

func TestHTTPClient(t *testing.T) {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	oc := &config.OriginConfig{Type: "TEST"}

	client := NewClient("test", oc, nil, logger)

	if client.HTTPClient() == nil {
		t.Errorf("missing http client")
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

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up`},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"15"},
		}).Encode(),
	}}
	client := &Client{logger: logger}
	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL})
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
}

func TestParseTimeRangeQueryMissingQuery(t *testing.T) {
	expected := errors.MissingURLParam(upQuery).Error()
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err == nil {
		t.Errorf(`expected "%s", got NO ERROR`, expected)
		return
	}
	if err.Error() != expected.Error() {
		t.Errorf(`expected "%s", got "%s"`, expected, err.Error())
	}
}

func TestParseTimeRangeQueryBadDuration(t *testing.T) {

	expected := `unable to parse duration: x`

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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	_, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
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
	client := &Client{logger: logger}
	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL})
	if err != nil {
		t.Error(err)
		return
	}

	if !res.IsOffset {
		t.Errorf("expected true got %t", res.IsOffset)
	}

}
