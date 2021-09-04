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

// Package health provides an application-wide health handler endpoint
// that is usually mapped to /trickster/health and provides the health
// status of the application's configured proxy endpoints
package health

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type healthDetail struct {
	text, json string
	mtx        sync.RWMutex
}

// StatusHandler returns an http.Handler that prints
// the real-time status of the provided Health Checker
// This handler spins up an infinitely looping background goroutine ("builder")
// that updates the status text in real-time. So long as the HealthChecker
// is closed with ShutDown(), the builder goroutine will exit
func StatusHandler(hc healthcheck.HealthChecker) http.Handler {
	if hc == nil {
		return nil
	}
	hd := &healthDetail{} // stores the status text in JSON and Text
	go builder(hc, hd)    // listens for rebuild notifications and updates the texts

	// the handler, when requested, simply prints out the static text stored in the healthDetail
	// which is being updated in real time by the builder.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body, ct string
		hd.mtx.RLock()
		if r != nil &&
			((r.Header != nil && r.Header.Get(headers.NameAccept) == headers.ValueApplicationJSON) ||
				(r.URL != nil && strings.Contains(r.URL.RawQuery, "json"))) {
			body = hd.json
			ct = headers.ValueApplicationJSON
		} else {
			body = hd.text
			ct = headers.ValueTextPlain
		}
		hd.mtx.RUnlock()
		w.Header().Set(headers.NameContentType, ct)
		w.WriteHeader(200)
		w.Write([]byte(body))
	})
}

func builder(hc healthcheck.HealthChecker, hd *healthDetail) {
	udpateStatusText(hc, hd) // setup the initial status page text
	notifier := make(chan bool, 32)
	for _, c := range hc.Statuses() {
		c.RegisterSubscriber(notifier)
	}
	closer := make(chan bool, 1)
	hc.Subscribe(closer)
	for {
		select {
		case <-closer: // a bool comes over closer when the Health Checker is closing down, so the builder should as well
			return
		case <-notifier: // a bool comes over notifier when the status text should be rebuilt
			hd.mtx.Lock()
			udpateStatusText(hc, hd)
			hd.mtx.Unlock()
		}
	}
}

const title = "Trickster Backend Health Status"

func udpateStatusText(hc healthcheck.HealthChecker, hd *healthDetail) {

	ut := time.Now().Truncate(time.Second).UTC().String()[:20] + "UTC"

	txt := strings.Builder{}
	json := strings.Builder{}

	txt.WriteString(fmt.Sprintf("\n%s            last change: %s\n", title, ut))
	json.WriteString(fmt.Sprintf(`{"title":"%s","udpateTime":"%s"`, title, ut))
	txt.WriteString("-------------------------------------------------------------------------------\n\n")
	st := hc.Statuses()
	b := bytes.NewBuffer(nil)
	tw := tabwriter.NewWriter(b, 10, 10, 3, ' ', 0)

	a := make([]string, 0, len(st))
	u := make([]string, 0, len(st))
	q := make([]string, 0, len(st))

	for k, v := range st {
		switch v.Get() {
		case 1:
			a = append(a, k)
		case -1:
			u = append(u, k)
		default:
			q = append(q, k)
		}

		sort.Strings(u)
		sort.Strings(q)

	}

	if len(a) > 0 {
		json.WriteString(`,"available":[`)
		sort.Strings(a)
		for i, k := range a {
			if i > 0 {
				json.WriteString(",")
			}
			d := cleanupDescription(st[k].Description())
			tw.Write([]byte(fmt.Sprintf("%s\t%s\t%s\n", k, d, statusToString(1))))
			json.WriteString(fmt.Sprintf(`{"name":"%s","provider":"%s"}`, k, d))
		}
		json.WriteString(`]`)
		tw.Write([]byte("\t\t\t\n"))
	}

	if len(u) > 0 {
		json.WriteString(`,"unavailable":[`)
		sort.Strings(u)
		for i, k := range u {
			if i > 0 {
				json.WriteString(",")
			}
			v := st[k]
			d := cleanupDescription(st[k].Description())
			fs := v.FailingSince().Truncate(time.Second).UTC().String()[:20] + "UTC"
			tw.Write([]byte(fmt.Sprintf("%s\t%s\t%s %s\n", k, d, statusToString(-1), fs)))
			json.WriteString(fmt.Sprintf(`{"name":"%s","provider":"%s","downSince":"%s","detail":"%s"}`,
				k, d, fs, strings.Replace(v.Detail(), `"`, `'`, -1)))
		}
		json.WriteString(`]`)
		tw.Write([]byte("\t\t\t\n"))
	}

	if len(q) > 0 {
		json.WriteString(`,"unchecked":[`)
		sort.Strings(a)
		for i, k := range q {
			if i > 0 {
				json.WriteString(",")
			}
			d := cleanupDescription(st[k].Description())
			tw.Write([]byte(fmt.Sprintf("%s\t%s\t%s\n", k, d, statusToString(0))))
			json.WriteString(fmt.Sprintf(`{"name":"%s","provider":"%s"}`, k, d))
		}
		json.WriteString(`]`)
		tw.Write([]byte("\n"))
	}

	tw.Flush()
	txt.Write(b.Bytes())
	txt.WriteString("-------------------------------------------------------------------------------\n")
	txt.WriteString(fmt.Sprintf("You can also provide a '%s: %s' Header or query param ?json\n",
		headers.NameAccept, headers.ValueApplicationJSON))
	json.WriteString("}")

	hd.text = txt.String()
	hd.json = json.String()
}

func statusToString(i int) string {
	if i > 0 {
		return "available"
	}
	if i < 0 {
		return "unavailable since"
	}
	return "not configured for automated health checks"
}

func cleanupDescription(in string) string {
	return strings.Replace(in, "reverseproxycache", "rpc", -1)
}
