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

// Package memberlist decodes the two member-list document formats that
// Trickster's file and http_sd providers accept.
//
// The native format is a list of members carrying everything a pool entry
// needs -- scheme, address, path prefix, weight, replica group:
//
//   - name: prom-1
//     scheme: https
//     address: 10.0.0.1:9090
//     path_prefix: /base
//     weight: 2
//
// The Prometheus format is the document Prometheus's own file_sd and
// http_sd consume, accepted so that an existing SD endpoint can be pointed
// at Trickster without rewriting it:
//
//	[{"targets": ["10.0.0.1:9090"], "labels": {"env": "prod"}}]
//
// It carries no weight or replica group, and its targets are bare
// host:port, so a scheme has to come from configuration. Deployments that
// need weighting or TSM replica groups want the native format.
package memberlist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"

	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"go.yaml.in/yaml/v3"
)

// SchemeHTTP and SchemeHTTPS are the accepted member schemes.
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// PrometheusSchemeLabel is the Prometheus meta-label that overrides the
// configured scheme for a target group. Honoring it lets one endpoint
// serve mixed-scheme members, as it does for Prometheus itself.
const PrometheusSchemeLabel = "__scheme__"

// Entry is one entry of a native-format member list.
type Entry struct {
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	Scheme     string `yaml:"scheme,omitempty" json:"scheme,omitempty"`
	Address    string `yaml:"address" json:"address"`
	PathPrefix string `yaml:"path_prefix,omitempty" json:"path_prefix,omitempty"`
	Weight     int    `yaml:"weight,omitempty" json:"weight,omitempty"`
	// ReplicaGroup optionally assigns the member to a TSM replica group
	ReplicaGroup string `yaml:"replica_group,omitempty" json:"replica_group,omitempty"`
}

// PrometheusGroup is one target group of a Prometheus file_sd/http_sd
// document.
type PrometheusGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// Parse converts a native-format member list into a Snapshot. The document
// may be YAML or JSON, since JSON is a subset of YAML. An empty document is
// an authoritative empty membership, not an error.
func Parse(b []byte) (discovery.Snapshot, error) {
	var entries []Entry
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &entries); err != nil {
			return nil, err
		}
	}
	out := make(discovery.Snapshot, 0, len(entries))
	for i, e := range entries {
		if e.Address == "" {
			return nil, fmt.Errorf("entry %d has no address", i)
		}
		if _, _, err := net.SplitHostPort(e.Address); err != nil {
			return nil, fmt.Errorf("entry %d address %q is not host:port",
				i, e.Address)
		}
		scheme := e.Scheme
		if scheme == "" {
			scheme = SchemeHTTP
		} else if scheme != SchemeHTTP && scheme != SchemeHTTPS {
			return nil, fmt.Errorf("entry %d scheme %q is not http or https",
				i, e.Scheme)
		}
		if e.Weight < 0 {
			return nil, fmt.Errorf("entry %d weight cannot be negative", i)
		}
		name := e.Name
		if name == "" {
			name = e.Address
		}
		out = append(out, discovery.Member{
			Name:         name,
			Scheme:       scheme,
			Address:      e.Address,
			PathPrefix:   e.PathPrefix,
			Weight:       e.Weight,
			ReplicaGroup: e.ReplicaGroup,
			Ready:        discovery.ReadyUnknown,
		})
	}
	return out, nil
}

// ParsePrometheus converts a Prometheus file_sd/http_sd document into a
// Snapshot, applying defaultScheme to every target whose group does not
// override it with a __scheme__ label. A group's labels are carried onto
// each of its members for observability.
//
// Unlike Parse, this is JSON only: Prometheus's http_sd contract specifies
// JSON, and accepting YAML here would mean silently tolerating documents no
// Prometheus-compatible server would emit.
func ParsePrometheus(b []byte, defaultScheme string) (discovery.Snapshot, error) {
	if defaultScheme == "" {
		defaultScheme = SchemeHTTP
	}
	var groups []PrometheusGroup
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &groups); err != nil {
			return nil, err
		}
	}
	out := make(discovery.Snapshot, 0, len(groups))
	for i, g := range groups {
		scheme := defaultScheme
		if s, ok := g.Labels[PrometheusSchemeLabel]; ok {
			if s != SchemeHTTP && s != SchemeHTTPS {
				return nil, fmt.Errorf("group %d %s %q is not http or https",
					i, PrometheusSchemeLabel, s)
			}
			scheme = s
		}
		for j, target := range g.Targets {
			if target == "" {
				return nil, fmt.Errorf("group %d target %d is empty", i, j)
			}
			if _, _, err := net.SplitHostPort(target); err != nil {
				return nil, fmt.Errorf("group %d target %q is not host:port",
					i, target)
			}
			out = append(out, discovery.Member{
				Name:    target,
				Scheme:  scheme,
				Address: target,
				Labels:  labelsFor(g.Labels),
				Ready:   discovery.ReadyUnknown,
			})
		}
	}
	return out, nil
}

// labelsFor copies a group's labels for one member, dropping the meta-label
// that has already been consumed as the scheme so it does not also show up
// as member metadata. Returns nil rather than an empty map so that members
// from unlabeled groups compare equal to members built without labels.
func labelsFor(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == PrometheusSchemeLabel {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
