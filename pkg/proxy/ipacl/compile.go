/*
 * Copyright 2026 The Trickster Authors
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

package ipacl

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Compile builds an immutable list from o.
//
// Files are read here. The returned warnings are for the loader to log;
// Compile does not log. On error the list is nil. Each warning is one line:
//
//	duplicate entry <prefix> (<where>)
//	<prefix> is listed as both allow and deny; deny wins (<where>)
//	unreachable rule "<input>" for <prefix> (<where>)
//	no entries and default deny; every address is denied
//
// The list name is included when Name is set. longest keeps every prefix.
// The longest match wins, and a deny wins when the same prefix is both
// allowed and denied. ordered is first match wins. Two CIDRs are nested or
// disjoint, never partially overlapping, so that is the same lookup once
// every rule an earlier rule covers completely is dropped. Check does not
// know which mode produced the list.
func Compile(o Options) (*List, []string, error) {
	o.Match = strings.TrimSpace(o.Match)
	o.Default = strings.TrimSpace(o.Default)
	o.Source = strings.TrimSpace(o.Source)
	o.Action = strings.TrimSpace(o.Action)
	if err := validate(o); err != nil {
		return nil, nil, err
	}
	c := &compiler{
		lead:    lead(o.Name),
		ordered: o.Match == matchOrdered,
		def:     defaultVerdict(o.Default),
		action:  listAction(o.Action),
		source:  listSource(o.Source),
		status:  listStatus(o.Status),
	}
	if err := c.collect(o); err != nil {
		return nil, nil, err
	}
	if c.entries == 0 && c.def == Deny {
		c.warnings = append(c.warnings, c.lead+": no entries and default deny; every address is denied")
	}
	return c.freeze(), c.warnings, nil
}

func lead(name string) string {
	if name == "" {
		return "ip acl"
	}
	return fmt.Sprintf("ip acl %q", name)
}

func defaultVerdict(s string) Verdict {
	if s == fallbackAllow {
		return Allow
	}
	return Deny
}

func listAction(s string) Action {
	if s == actionDrop {
		return Drop
	}
	return Reject
}

func listSource(s string) Source {
	if s == sourcePeer {
		return Peer
	}
	return ClientIP
}

func listStatus(status int) int {
	if status == 0 {
		return DefaultStatus
	}
	return status
}

func validate(o Options) error {
	switch o.Match {
	case "", matchLongest:
		if len(o.Rules) > 0 {
			return fmt.Errorf("%w: rules require match: ordered", ErrInvalidMatch)
		}
	case matchOrdered:
		if len(o.Allow) > 0 || len(o.Deny) > 0 ||
			strings.TrimSpace(o.AllowFile) != "" || strings.TrimSpace(o.DenyFile) != "" {
			return fmt.Errorf("%w: ordered uses rules, not allow, deny or files", ErrInvalidMatch)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidMatch, o.Match)
	}
	switch o.Default {
	case "", fallbackDeny, fallbackAllow:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDefault, o.Default)
	}
	switch o.Source {
	case "", sourceClientIP, sourcePeer:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSource, o.Source)
	}
	switch o.Action {
	case "", actionReject, actionDrop:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAction, o.Action)
	}
	if o.Status != 0 && (o.Status < 400 || o.Status > 599) {
		return fmt.Errorf("%w: %d", ErrInvalidStatus, o.Status)
	}
	if o.Match == matchOrdered {
		for i := range o.Rules {
			if _, _, n := ruleValue(o.Rules[i]); n != 1 {
				return fmt.Errorf("%w: rule %d must set exactly one of allow, deny, allow_file or deny_file",
					ErrInvalidRule, i+1)
			}
		}
	}
	return nil
}

// ruleValue reports the single field a rule sets. n is how many were set.
func ruleValue(r Rule) (kind, value string, n int) {
	type field struct{ kind, value string }
	for _, f := range []field{
		{"allow", strings.TrimSpace(r.Allow)},
		{"deny", strings.TrimSpace(r.Deny)},
		{"allow_file", strings.TrimSpace(r.AllowFile)},
		{"deny_file", strings.TrimSpace(r.DenyFile)},
	} {
		if f.value == "" {
			continue
		}
		kind, value = f.kind, f.value
		n++
	}
	return kind, value, n
}

type compiler struct {
	lead     string
	ordered  bool
	def      Verdict
	action   Action
	source   Source
	status   int
	v4, v6   familyBuilder
	warnings []string
	entries  int
}

func (c *compiler) collect(o Options) error {
	if c.ordered {
		return c.collectRules(o.Rules)
	}
	for _, raw := range o.Allow {
		if err := c.addEntry(raw, Allow, "allow"); err != nil {
			return err
		}
	}
	for _, raw := range o.Deny {
		if err := c.addEntry(raw, Deny, "deny"); err != nil {
			return err
		}
	}
	if path := strings.TrimSpace(o.AllowFile); path != "" {
		if err := c.addFile(path, Allow, fmt.Sprintf("allow_file %q", path)); err != nil {
			return err
		}
	}
	if path := strings.TrimSpace(o.DenyFile); path != "" {
		if err := c.addFile(path, Deny, fmt.Sprintf("deny_file %q", path)); err != nil {
			return err
		}
	}
	return nil
}

func (c *compiler) collectRules(rules []Rule) error {
	for i := range rules {
		kind, value, _ := ruleValue(rules[i])
		origin := fmt.Sprintf("rule %d %s", i+1, kind)
		switch kind {
		case "allow":
			if err := c.addEntry(value, Allow, origin); err != nil {
				return err
			}
		case "deny":
			if err := c.addEntry(value, Deny, origin); err != nil {
				return err
			}
		case "allow_file":
			if err := c.addFile(value, Allow, fmt.Sprintf("rule %d allow_file %q", i+1, value)); err != nil {
				return err
			}
		case "deny_file":
			if err := c.addFile(value, Deny, fmt.Sprintf("rule %d deny_file %q", i+1, value)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *compiler) addFile(path string, v Verdict, origin string) error {
	lines, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", c.lead, err, origin)
	}
	for _, ln := range lines {
		if err := c.addEntry(ln.text, v, fmt.Sprintf("%s line %d", origin, ln.n)); err != nil {
			return err
		}
	}
	return nil
}

func (c *compiler) addEntry(raw string, v Verdict, origin string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	prefixes, err := parseEntry(raw)
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", c.lead, err, origin)
	}
	c.entries++
	for _, p := range prefixes {
		c.add(p, v, raw, origin)
	}
	return nil
}

func (c *compiler) add(p netip.Prefix, v Verdict, raw, origin string) {
	b := c.builder(p)
	if c.ordered {
		// An earlier rule that contains p, including an equal prefix, already
		// decided this address. Dropping p leaves first-match equal to a
		// longest-prefix lookup over what remains.
		if b.covers(p) {
			c.warnings = append(c.warnings, fmt.Sprintf(
				"%s: unreachable rule %q for %s (%s)", c.lead, raw, p, origin))
			return
		}
		b.insert(p, v)
		return
	}
	dup, conflict := b.insert(p, v)
	switch {
	case dup:
		c.warnings = append(c.warnings, fmt.Sprintf(
			"%s: duplicate entry %s (%s)", c.lead, p, origin))
	case conflict:
		c.warnings = append(c.warnings, fmt.Sprintf(
			"%s: %s is listed as both allow and deny; deny wins (%s)", c.lead, p, origin))
	}
}

func (c *compiler) builder(p netip.Prefix) *familyBuilder {
	if p.Addr().Is4() {
		return &c.v4
	}
	return &c.v6
}

func (c *compiler) freeze() *List {
	return &List{
		v4:     c.v4.freeze(),
		v6:     c.v6.freeze(),
		def:    c.def,
		action: c.action,
		source: c.source,
		status: c.status,
	}
}

// familyBuilder collects prefixes for one family. Its maps are not written
// after freeze, which is what makes Check lock-free.
type familyBuilder struct {
	tables []lengthTable
	index  map[int]int
}

func (b *familyBuilder) insert(p netip.Prefix, v Verdict) (dup, conflict bool) {
	bits := p.Bits()
	if b.index == nil {
		b.index = make(map[int]int)
	}
	i, ok := b.index[bits]
	if !ok {
		i = len(b.tables)
		b.index[bits] = i
		b.tables = append(b.tables, lengthTable{
			bits:   bits,
			byAddr: make(map[netip.Addr]Verdict),
		})
	}
	key := p.Addr()
	old, exists := b.tables[i].byAddr[key]
	if !exists {
		b.tables[i].byAddr[key] = v
		return false, false
	}
	if old == v {
		return true, false
	}
	b.tables[i].byAddr[key] = Deny
	return false, true
}

// covers reports whether an earlier prefix contains the whole of p.
// A longer prefix cannot cover a shorter one, so only lengths of p or
// less are consulted. The address is already the network address.
func (b *familyBuilder) covers(p netip.Prefix) bool {
	bits := p.Bits()
	addr := p.Addr()
	for i := range b.tables {
		bl := b.tables[i].bits
		if bl > bits {
			continue
		}
		if _, ok := b.tables[i].byAddr[maskedAddr(addr, bl)]; ok {
			return true
		}
	}
	return false
}

func (b *familyBuilder) freeze() family {
	tables := append([]lengthTable(nil), b.tables...)
	slices.SortFunc(tables, func(a, c lengthTable) int {
		return cmp.Compare(c.bits, a.bits)
	})
	return family{tables: tables}
}
