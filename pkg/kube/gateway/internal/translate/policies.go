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

package translate

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

// PolicySource answers which cache policy governs a target, and whether a policy's provider
// predefines a path a rule declares; a nil source governs nothing
type PolicySource interface {
	Lookup(kind, namespace, name, section string) (*ir.Policy, bool)
	ProviderConflict(p *ir.Policy, matches []ir.Match) (string, bool)
}

// Target kinds a cache policy names, spelled as its targetRefs spell them
const (
	TargetGateway   = ir.KindGateway
	TargetHTTPRoute = ir.KindHTTPRoute
	TargetService   = "Service"
	TargetIngress   = ir.KindIngress
)

// Policies holds the policies a model names, each once: those the translator read and those
// it minted by combining them, since a rule names exactly one and several may govern it
type Policies struct {
	model    *ir.IR
	named    map[string]*ir.Policy
	source   PolicySource
	problems *Problems
}

// mergeSeparator joins the names of the policies a minted one combines; no policy name
// holds it, since they are object keys
const mergeSeparator = "+"

// withoutProviderSuffix marks a minted policy that is another with its provider withheld
const withoutProviderSuffix = mergeSeparator + "noprovider"

// NewPolicies returns an empty collection appending to the model, attaching the source's
// policies and reporting through problems
func NewPolicies(model *ir.IR, source PolicySource, problems *Problems) *Policies {
	return &Policies{
		model: model, named: make(map[string]*ir.Policy), source: source, problems: problems,
	}
}

// Add appends the policy to the model the first time its name is seen, and returns the name
func (m *Policies) Add(p ir.Policy) string {
	if _, ok := m.named[p.Name]; ok {
		return p.Name
	}
	c := p.Clone()
	m.named[p.Name] = &c
	m.model.Policies = append(m.model.Policies, c)
	return p.Name
}

// Get returns the named policy, nil for one the model does not hold
func (m *Policies) Get(name string) *ir.Policy {
	return m.named[name]
}

// Attach returns the name of the cache policy governing a target, added to the model, or
// nothing when none does
func (m *Policies) Attach(kind, namespace, name, section string) string {
	if m.source == nil {
		return ""
	}
	p, ok := m.source.Lookup(kind, namespace, name, section)
	if !ok {
		return ""
	}
	return m.Add(*p)
}

// Merge returns the policy combining the named ones, least specific first, minting it the first
// time the combination is asked for; empty names are skipped and one name is returned as is
func (m *Policies) Merge(names ...string) (string, *ir.Policy) {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" && m.named[n] != nil {
			parts = append(parts, n)
		}
	}
	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		return parts[0], m.named[parts[0]]
	}
	name := strings.Join(parts, mergeSeparator)
	if p, ok := m.named[name]; ok {
		return name, p
	}
	merged := *m.named[parts[0]]
	for _, n := range parts[1:] {
		merged = merged.Overlay(m.named[n])
	}
	// the most specific part is the one that made the combination distinct
	merged.Name, merged.Source = name, m.named[parts[len(parts)-1]].Source
	m.Add(merged)
	return name, m.named[name]
}

// WithoutProvider returns the named policy with its provider withheld, minted once; a policy
// naming none is returned as is
func (m *Policies) WithoutProvider(name string) (string, *ir.Policy) {
	p := m.named[name]
	if p == nil || p.Provider == "" {
		return name, p
	}
	stripped := name + withoutProviderSuffix
	if s, ok := m.named[stripped]; ok {
		return stripped, s
	}
	c := p.Clone()
	c.Name, c.Provider = stripped, ""
	m.Add(c)
	return stripped, m.named[stripped]
}

// Bind returns the policy a rule names: the parts merged least specific first, with a provider
// that predefines one of the rule's own paths withheld, since the rule's path would otherwise
// replace the provider's handler for it; the withholding is reported on the route and the policy
func (m *Policies) Bind(src ir.Source, ruleIdx int, matches []ir.Match, parts ...string) string {
	name, p := m.Merge(parts...)
	if p == nil || p.Provider == "" || m.source == nil {
		return name
	}
	path, conflict := m.source.ProviderConflict(p, matches)
	if !conflict {
		return name
	}
	if m.problems != nil {
		m.problems.Reject(src, "rule %d: provider %q is not applied: the rule declares path %q, "+
			"which the provider predefines", ruleIdx, p.Provider, path)
		m.problems.Reject(p.Source, "%s rule %d declares path %q, which provider %q predefines; "+
			"the provider is not applied there", src.Key(), ruleIdx, path, p.Provider)
	}
	stripped, _ := m.WithoutProvider(name)
	return stripped
}

// BindMember returns the policy one member names: the policies on its Service and on the port
// it addresses, the port's over the Service's, bound as Bind binds a rule's
func (m *Policies) BindMember(src ir.Source, ruleIdx int, matches []ir.Match,
	target ir.ServiceTarget,
) string {
	whole := m.Attach(TargetService, target.Namespace, target.Name, "")
	var port string
	if target.PortName != "" {
		port = m.Attach(TargetService, target.Namespace, target.Name, target.PortName)
	}
	return m.Bind(src, ruleIdx, matches, whole, port)
}
