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

package nomad

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
)

// serviceRegistration is one element of a /v1/service/:name response. Only
// the fields Trickster maps are declared; decoding Nomad's full schema would
// make every upstream addition a potential incompatibility here.
type serviceRegistration struct {
	ID          string   `json:"ID"`
	ServiceName string   `json:"ServiceName"`
	Address     string   `json:"Address"`
	Port        int      `json:"Port"`
	Tags        []string `json:"Tags"`
	Namespace   string   `json:"Namespace"`
	Datacenter  string   `json:"Datacenter"`
	JobID       string   `json:"JobID"`
	AllocID     string   `json:"AllocID"`
	NodeID      string   `json:"NodeID"`
}

// mapping carries the query-derived choices that turn registrations into
// members.
type mapping struct {
	scheme string
	// tags, when non-empty, keeps only registrations carrying every listed
	// tag. Nomad's service endpoint has no tag parameter, unlike Consul's,
	// so this filter is applied here rather than server-side.
	tags []string
}

// parseRegistry decodes a service-registration response into a Snapshot.
//
// A registration that cannot yield a usable address is an error rather than
// a skip: silently dropping members is how a pool quietly shrinks, and a
// registry that has started emitting something unexpected should surface as
// a refresh failure that keeps last-good, not as a smaller membership.
func parseRegistry(body []byte, m mapping) (discovery.Snapshot, error) {
	var regs []serviceRegistration
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &regs); err != nil {
			return nil, err
		}
	}
	out := make(discovery.Snapshot, 0, len(regs))
	for i, r := range regs {
		if !r.hasAllTags(m.tags) {
			continue
		}
		member, err := r.toMember(m)
		if err != nil {
			return nil, fmt.Errorf("registration %d: %w", i, err)
		}
		out = append(out, member)
	}
	return out, nil
}

// hasAllTags reports whether the registration carries every required tag.
func (r *serviceRegistration) hasAllTags(required []string) bool {
	for _, tag := range required {
		if !slices.Contains(r.Tags, tag) {
			return false
		}
	}
	return true
}

// toMember maps one registration onto a pool member.
func (r *serviceRegistration) toMember(m mapping) (discovery.Member, error) {
	if r.Address == "" {
		return discovery.Member{}, fmt.Errorf(
			"service %q has no address", r.ID)
	}
	if r.Port <= 0 || r.Port > 65535 {
		return discovery.Member{}, fmt.Errorf(
			"service %q has no usable port (%d)", r.ID, r.Port)
	}
	name := r.ID
	if name == "" {
		name = r.ServiceName
	}
	return discovery.Member{
		Name:    name,
		Scheme:  m.scheme,
		Address: net.JoinHostPort(r.Address, strconv.Itoa(r.Port)),
		// Nomad's native registry conveys no per-instance health, so
		// readiness is genuinely unknown rather than assumed good. Pools
		// using health_mode: provider fall back to their own probes; jobs
		// that register into Consul instead should use the consul provider,
		// which does convey health.
		Ready:  discovery.ReadyUnknown,
		Labels: r.labels(),
	}, nil
}

// labels carries registry metadata onto the member for observability. The
// allocation and job identifiers are the ones an operator needs to trace a
// member back to the workload that registered it.
func (r *serviceRegistration) labels() map[string]string {
	out := map[string]string{
		"service":    r.ServiceName,
		"service_id": r.ID,
		"job_id":     r.JobID,
		"alloc_id":   r.AllocID,
		"node_id":    r.NodeID,
		"namespace":  r.Namespace,
		"datacenter": r.Datacenter,
	}
	if len(r.Tags) > 0 {
		// bracketing separators make a tag match unambiguous for anything
		// that later filters on this string
		out["tags"] = "," + strings.Join(r.Tags, ",") + ","
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
