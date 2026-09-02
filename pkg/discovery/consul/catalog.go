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

package consul

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
)

// Consul health check statuses, worst-last so that aggregation is a max.
const (
	statusPassing     = "passing"
	statusWarning     = "warning"
	statusCritical    = "critical"
	statusMaintenance = "maintenance"
)

// serviceEntry is one element of a /v1/health/service/:service response.
// Only the fields Trickster maps are declared; Consul's response carries a
// great deal more, and decoding it all would make every schema addition
// upstream a potential incompatibility here.
type serviceEntry struct {
	Node    node    `json:"Node"`
	Service service `json:"Service"`
	Checks  []check `json:"Checks"`
}

type node struct {
	Node       string            `json:"Node"`
	Address    string            `json:"Address"`
	Datacenter string            `json:"Datacenter"`
	Meta       map[string]string `json:"Meta"`
}

type service struct {
	ID      string            `json:"ID"`
	Service string            `json:"Service"`
	Tags    []string          `json:"Tags"`
	Address string            `json:"Address"`
	Port    int               `json:"Port"`
	Meta    map[string]string `json:"Meta"`
	Weights *weights          `json:"Weights"`
}

// weights carries Consul's own load-balancing weights, which it applies to
// DNS SRV answers. Mapping them onto member weights means an operator who
// has already expressed relative capacity to Consul does not have to
// express it again to Trickster.
type weights struct {
	Passing int `json:"Passing"`
	Warning int `json:"Warning"`
}

type check struct {
	CheckID string `json:"CheckID"`
	Status  string `json:"Status"`
}

// mapping carries the query-derived choices that turn catalog entries into
// members.
type mapping struct {
	scheme            string
	replicaGroupLabel string
	warningIsReady    bool
}

// parseCatalog decodes a health-service response into a Snapshot.
//
// An entry that cannot yield a usable address is an error rather than a
// skip: silently dropping members is how a pool quietly shrinks, and a
// catalog that has started emitting something unexpected should surface as
// a refresh failure that keeps last-good, not as a smaller membership.
func parseCatalog(body []byte, m mapping) (discovery.Snapshot, error) {
	var entries []serviceEntry
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, err
		}
	}
	out := make(discovery.Snapshot, 0, len(entries))
	for i, e := range entries {
		member, err := e.toMember(m)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		out = append(out, member)
	}
	return out, nil
}

// toMember maps one catalog entry onto a pool member.
func (e *serviceEntry) toMember(m mapping) (discovery.Member, error) {
	// Consul's rule: a service that registers its own address overrides the
	// node's, which is how sidecars and containers with their own routable
	// address are represented
	address := e.Service.Address
	if address == "" {
		address = e.Node.Address
	}
	if address == "" {
		return discovery.Member{}, fmt.Errorf(
			"service %q has neither a service nor a node address", e.Service.ID)
	}
	if e.Service.Port <= 0 || e.Service.Port > 65535 {
		return discovery.Member{}, fmt.Errorf(
			"service %q has no usable port (%d)", e.Service.ID, e.Service.Port)
	}
	status := e.aggregateStatus()
	name := e.Service.ID
	if name == "" {
		name = e.Service.Service
	}
	return discovery.Member{
		Name:         name,
		Scheme:       m.scheme,
		Address:      net.JoinHostPort(address, strconv.Itoa(e.Service.Port)),
		Weight:       e.weightFor(status),
		ReplicaGroup: e.replicaGroup(m.replicaGroupLabel),
		Ready:        readyFor(status, m.warningIsReady),
		Labels:       e.labels(status),
	}, nil
}

// aggregateStatus returns the worst status among the entry's checks, which
// is how Consul itself decides whether an instance is serving. No checks at
// all means passing: a service registered without checks is not unhealthy,
// it is unmonitored.
func (e *serviceEntry) aggregateStatus() string {
	worst := statusPassing
	for _, c := range e.Checks {
		if severity(c.Status) > severity(worst) {
			worst = c.Status
		}
	}
	return worst
}

// severity orders statuses so aggregation is a max.
func severity(status string) int {
	switch status {
	case statusPassing:
		return 0
	case statusWarning:
		return 1
	case statusMaintenance:
		// maintenance is an operator deliberately taking an instance out,
		// which is at least as strong a signal as a failing check
		return 2
	case statusCritical:
		return 3
	default:
		// an unrecognized status is treated as the worst case rather than
		// ignored, so a Consul release that adds one cannot silently put
		// unhealthy members into pools
		return 4
	}
}

// readyFor maps an aggregate status onto the member's readiness.
func readyFor(status string, warningIsReady bool) discovery.ReadyState {
	switch status {
	case statusPassing:
		return discovery.Ready
	case statusWarning:
		if warningIsReady {
			return discovery.Ready
		}
		return discovery.NotReady
	default:
		return discovery.NotReady
	}
}

// weightFor returns the Consul weight matching the entry's status. Consul
// keeps separate passing and warning weights so that a degraded instance can
// be given less traffic without being removed; that distinction is preserved
// here rather than flattened.
func (e *serviceEntry) weightFor(status string) int {
	if e.Service.Weights == nil {
		return 0 // unweighted; the pool treats it as 1
	}
	w := e.Service.Weights.Passing
	if status == statusWarning {
		w = e.Service.Weights.Warning
	}
	if w < 0 {
		return 0
	}
	return w
}

// replicaGroup reads the TSM replica group from service metadata, when the
// query names a key to read it from.
func (e *serviceEntry) replicaGroup(key string) string {
	if key == "" {
		return ""
	}
	return e.Service.Meta[key]
}

// labels carries catalog metadata onto the member for observability. Service
// metadata is prefixed so that a user-defined key cannot shadow a
// Trickster-assigned one.
func (e *serviceEntry) labels(status string) map[string]string {
	out := map[string]string{
		"service":    e.Service.Service,
		"service_id": e.Service.ID,
		"node":       e.Node.Node,
		"datacenter": e.Node.Datacenter,
		"status":     status,
	}
	if len(e.Service.Tags) > 0 {
		// the leading and trailing separators make a tag match unambiguous
		// for anything that later filters on this string, following the
		// convention Prometheus uses for the same reason
		out["tags"] = "," + strings.Join(e.Service.Tags, ",") + ","
	}
	for k, v := range e.Service.Meta {
		out["meta_"+k] = v
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
