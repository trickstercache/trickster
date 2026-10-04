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
	"fmt"
	"net/netip"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/prefixtable"
)

// Compile builds an immutable list from o, reading its files, and returns warnings to log. A deny beats an
// equal allow, and an ordered rule that an earlier one covers is dropped
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

func ruleValue(r Rule) (kind, value string, n int) {
	// the single field a rule sets, and n, how many it sets
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
	prefixes prefixtable.Builder[Verdict]
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
	if c.ordered {
		// an earlier rule containing p, an equal prefix included, already decided its addresses; dropping p keeps
		// first match equal to the longest-prefix lookup over what remains
		if c.prefixes.Covers(p) {
			c.warnings = append(c.warnings, fmt.Sprintf(
				"%s: unreachable rule %q for %s (%s)", c.lead, raw, p, origin))
			return
		}
		c.prefixes.Set(p, v)
		return
	}
	old, exists := c.prefixes.Set(p, v)
	switch {
	case !exists:
	case old == v:
		c.warnings = append(c.warnings, fmt.Sprintf(
			"%s: duplicate entry %s (%s)", c.lead, p, origin))
	default:
		c.prefixes.Set(p, Deny)
		c.warnings = append(c.warnings, fmt.Sprintf(
			"%s: %s is listed as both allow and deny; deny wins (%s)", c.lead, p, origin))
	}
}

func (c *compiler) freeze() *List {
	return &List{
		prefixes: c.prefixes.Table(),
		def:      c.def,
		action:   c.action,
		source:   c.source,
		status:   c.status,
	}
}
