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
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/contenttype"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type detail struct {
	text, json, yaml string
	lastModified     time.Time
}

type healthDetail struct {
	detail atomic.Pointer[detail]
}

type backendStatus struct {
	Name                   string   `json:"name" yaml:"name"`
	Provider               string   `json:"provider" yaml:"provider"`
	DownSince              string   `json:"downSince,omitempty" yaml:"downSince,omitempty"`
	Detail                 string   `json:"detail,omitempty" yaml:"detail,omitempty"`
	Mechanism              string   `json:"mechanism,omitempty" yaml:"mechanism,omitempty"`
	AvailablePoolMembers   []string `json:"availablePoolMembers,omitempty" yaml:"availablePoolMembers,omitempty"`
	UnavailablePoolMembers []string `json:"unavailablePoolMembers,omitempty" yaml:"unavailablePoolMembers,omitempty"`
	UncheckedPoolMembers   []string `json:"uncheckedPoolMembers,omitempty" yaml:"uncheckedPoolMembers,omitempty"`
}

type healthStatus struct {
	Title       string          `json:"title" yaml:"title"`
	UpdateTime  string          `json:"updateTime" yaml:"updateTime"`
	Unavailable []backendStatus `json:"unavailable,omitempty" yaml:"unavailable,omitempty"`
	Available   []backendStatus `json:"available,omitempty" yaml:"available,omitempty"`
	Unchecked   []backendStatus `json:"unchecked,omitempty" yaml:"unchecked,omitempty"`
}

var updateLock sync.Mutex

func (hs *healthStatus) String() string {
	return hs.Tabular()
}

func (hs *healthStatus) JSON() string {
	b, err := json.Marshal(hs)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (hs *healthStatus) YAML() string {
	b, err := yaml.Marshal(hs)
	if err != nil {
		return "---"
	}
	return string(b)
}

// Tabular renders a text/plain-compatible version of the status page
func (hs *healthStatus) Tabular() string {
	txt := &strings.Builder{}
	fmt.Fprintf(txt, "\n%s            last change: %s\n", hs.Title, hs.UpdateTime)
	txt.WriteString("-------------------------------------------------------------------------------\n\n")

	b := bytes.NewBuffer(nil)
	tw := tabwriter.NewWriter(b, 10, 10, 3, ' ', 0)

	if len(hs.Unavailable) > 0 {
		for _, k := range hs.Unavailable {
			fmt.Fprintf(tw, "%s\t%s\t%s %s%s\n", k.Name, formatProvider(k),
				statusToString(-1, k.DownSince != ""), k.DownSince, formatDetail(k))
		}
		tw.Write([]byte("\t\t\t\n"))
	}

	if len(hs.Available) > 0 {
		for _, k := range hs.Available {
			fmt.Fprintf(tw, "%s\t%s\t%s%s\n", k.Name, formatProvider(k),
				statusToString(1, false), formatDetail(k))
		}
		tw.Write([]byte("\t\t\t\n"))
	}

	if len(hs.Unchecked) > 0 {
		for _, k := range hs.Unchecked {
			fmt.Fprintf(tw, "%s\t%s\t%s%s\n", k.Name, formatProvider(k),
				statusToString(0, false), formatDetail(k))
		}
		tw.Write([]byte("\n"))
	}

	tw.Flush()
	txt.Write(b.Bytes())
	txt.WriteString("-------------------------------------------------------------------------------\n")
	fmt.Fprintf(txt, "For JSON, provide a '%s: %s' Header or query param ?json\n",
		headers.NameAccept, headers.ValueApplicationJSON)
	fmt.Fprintf(txt, "For YAML, provide a '%s: %s' Header or query param ?yaml\n",
		headers.NameAccept, headers.ValueApplicationYAML)

	return txt.String()
}

// StatusHandler returns an http.Handler that prints
// the real-time status of the provided Health Checker
// This handler spins up an infinitely looping background goroutine ("builder")
// that updates the status text in real-time. So long as the HealthChecker
// is closed with ShutDown(), the builder goroutine will exit
func StatusHandler(hc healthcheck.HealthChecker, backends backends.Backends) http.Handler {
	if hc == nil {
		return nil
	}
	hd := &healthDetail{}        // stores the status text in JSON and Text
	go builder(hc, hd, backends) // listens for rebuild notifications and updates the texts

	// the handler, when requested, simply prints out the static text stored in the healthDetail
	// which is being updated in real time by the builder.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detail := hd.detail.Load()
		// return 304 if client's cached version is still fresh
		if ims := r.Header.Get(headers.NameIfModifiedSince); ims != "" && !detail.lastModified.IsZero() {
			if ifModifiedSince, err := time.Parse(time.RFC1123, ims); err == nil {
				if !detail.lastModified.After(ifModifiedSince) {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		}

		var body, ct string
		switch {
		case headers.AcceptsJSON(r),
			(r != nil && r.URL != nil &&
				strings.Contains(r.URL.RawQuery, contenttype.JSON)):
			body = detail.json
			ct = headers.ValueApplicationJSON
		case headers.AcceptsYAML(r),
			(r != nil && r.URL != nil && strings.Contains(strings.ToLower(r.URL.RawQuery), "yaml")):
			body = detail.yaml
			ct = headers.ValueApplicationYAML
		default:
			body = detail.text
			ct = headers.ValueTextPlain
		}
		w.Header().Set(headers.NameContentType, ct)
		if !detail.lastModified.IsZero() {
			w.Header().Set(headers.NameLastModified, detail.lastModified.Format(time.RFC1123))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	})
}

func builder(hc healthcheck.HealthChecker, hd *healthDetail, backends backends.Backends) {
	updateStatusText(hc, hd, backends) // setup the initial status page text
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
			updateStatusText(hc, hd, backends)
		}
	}
}

const title = "Trickster Backend Health Status"

func updateStatusText(hc healthcheck.HealthChecker, hd *healthDetail, backends backends.Backends) {
	updateLock.Lock()
	defer updateLock.Unlock()

	// HTTP Spec prefers GMT in RFC1123 Headers
	lastModified := time.Now().Truncate(time.Second).In(time.FixedZone("GMT", 0))
	// use UTC in the response body
	ut := lastModified.String()[:20] + "UTC"

	status := &healthStatus{
		Title:      title,
		UpdateTime: ut,
	}

	st := hc.Statuses()

	a := make([]string, len(st))
	u := make([]string, len(st))
	q := make([]string, len(st))
	var al, ul, ql int

	for k, v := range st {
		switch v.Get() {
		case 1:
			a[al] = k
			al++
		case -1:
			u[ul] = k
			ul++
		default:
			q[ql] = k
			ql++
		}
	}

	a = a[:al]
	u = u[:ul]
	q = q[:ql]

	if len(a) > 0 {
		sort.Strings(a)
		status.Available = make([]backendStatus, len(a))
		for i, k := range a {
			d := cleanupDescription(st[k].Description())
			status.Available[i] = backendStatus{
				Name:     k,
				Provider: d,
			}
		}
	}

	if len(u) > 0 {
		sort.Strings(u)
		status.Unavailable = make([]backendStatus, len(u))
		for i, k := range u {
			v := st[k]
			d := cleanupDescription(st[k].Description())
			fs := v.FailingSince().Truncate(time.Second).UTC().String()[:20] + "UTC"
			status.Unavailable[i] = backendStatus{
				Name:      k,
				Provider:  d,
				DownSince: fs,
				Detail:    v.Detail(),
			}
		}
	}

	if len(q) > 0 {
		sort.Strings(q)
		status.Unchecked = make([]backendStatus, len(q))
		for i, k := range q {
			d := cleanupDescription(st[k].Description())
			status.Unchecked[i] = backendStatus{
				Name:     k,
				Provider: d,
			}
		}
	}

	// process ALB backends
	if backends != nil {
		albNames := make([]string, 0)
		for name, backend := range backends {
			if backend != nil && backend.Configuration() != nil &&
				backend.Configuration().Provider == providers.ALB {
				albNames = append(albNames, name)
			}
		}
		sort.Strings(albNames)

		for _, albName := range albNames {
			albBackend := backends[albName]
			albClient, ok := albBackend.(*alb.Client)
			if !ok {
				continue
			}
			albConfig := albClient.Configuration()
			if albConfig == nil || albConfig.ALBOptions == nil {
				continue
			}

			// get pool members w/ health status
			availableMembers := make([]string, 0)
			unavailableMembers := make([]string, 0)
			uncheckedMembers := make([]string, 0)

			for _, poolMemberName := range albConfig.ALBOptions.Pool {
				memberStatus := st[poolMemberName]
				if memberStatus == nil {
					uncheckedMembers = append(uncheckedMembers, poolMemberName)
					continue
				}
				memberHealth := memberStatus.Get()
				switch {
				case memberHealth >= 1:
					availableMembers = append(availableMembers, poolMemberName)
				case memberHealth < 0:
					unavailableMembers = append(unavailableMembers, poolMemberName)
				default:
					uncheckedMembers = append(uncheckedMembers, poolMemberName)
				}
			}
			sort.Strings(availableMembers)
			sort.Strings(unavailableMembers)
			sort.Strings(uncheckedMembers)

			albStatus := backendStatus{
				Name:                   albName,
				Provider:               providers.ALB,
				Mechanism:              albConfig.ALBOptions.MechanismName,
				AvailablePoolMembers:   availableMembers,
				UnavailablePoolMembers: unavailableMembers,
				UncheckedPoolMembers:   uncheckedMembers,
			}

			// ALB is "available" if >= 1 pool member is either available or unchecked
			if len(availableMembers) > 0 || len(uncheckedMembers) > 0 {
				status.Available = append(status.Available, albStatus)
			} else {
				status.Unavailable = append(status.Unavailable, albStatus)
			}
		}
	}

	hd.detail.Store(&detail{text: status.Tabular(), json: status.JSON(),
		yaml: status.YAML(), lastModified: lastModified})
}

func statusToString(i int, hasSince bool) string {
	if i > 0 {
		return "available"
	}
	if i < 0 {
		if hasSince {
			return "unavailable since"
		}
		return "unavailable"
	}
	return "not configured for automated health checks"
}

func formatProvider(bs backendStatus) string {
	if bs.Provider == providers.ALB && bs.Mechanism != "" {
		return fmt.Sprintf("%s (%s)", bs.Provider, bs.Mechanism)
	}
	return bs.Provider
}

func formatDetail(bs backendStatus) string {
	if bs.Provider != providers.ALB {
		return ""
	}
	parts := make([]string, 0, 3)
	if len(bs.UnavailablePoolMembers) > 0 {
		parts = append(parts, fmt.Sprintf("u:[%s]", strings.Join(bs.UnavailablePoolMembers, ", ")))
	}
	if len(bs.AvailablePoolMembers) > 0 {
		parts = append(parts, fmt.Sprintf("a:[%s]", strings.Join(bs.AvailablePoolMembers, ", ")))
	}
	if len(bs.UncheckedPoolMembers) > 0 {
		parts = append(parts, fmt.Sprintf("nc:[%s]", strings.Join(bs.UncheckedPoolMembers, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func cleanupDescription(in string) string {
	return strings.ReplaceAll(in, providers.ReverseProxyCache,
		providers.ReverseProxyCacheShort)
}
