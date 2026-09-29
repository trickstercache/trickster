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

package alb

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const noStepAlignment = "none" // names a member that applies no step alignment

type memberAlignment struct {
	name                  string
	effective, applicable timeseries.StepAlignment
}

func followsLeader(o *ao.Options) bool {
	// without its own mode, a merging ALB applies its first configured member's mode to every member,
	// so that all of them answer on one grid
	return o != nil && o.MechanismName == names.MechanismTSM
}

func appliesStepAlignment(o *bo.Options) bool {
	return o != nil && o.ALBOptions != nil && (o.StepAlignment != 0 || followsLeader(o.ALBOptions))
}

func resolveStepAlignment(configured timeseries.StepAlignment, leader bool,
	members []memberAlignment,
) (timeseries.StepAlignment, []string) {
	// the ALB's own mode, else its leader's; members whose applicable modes lack it are returned
	mode := configured
	if mode == 0 && leader && len(members) > 0 {
		mode = members[0].effective
	}
	if mode == 0 {
		return 0, nil
	}
	var lacking []string
	for _, m := range members {
		if m.applicable&mode == 0 {
			lacking = append(lacking, m.name)
		}
	}
	return mode, lacking
}

func allowedStepAlignments(members []memberAlignment) timeseries.StepAlignment {
	// the modes every member supports, which a query's directive may choose over the ALB's mode
	if len(members) == 0 {
		return 0
	}
	allowed := timeseries.StepAlignmentAll
	for _, m := range members {
		allowed &= m.applicable
	}
	return allowed
}

func fallbackStepAlignment(members []memberAlignment) timeseries.StepAlignment {
	// truncate when every member applies it, else none, which leaves each member to its own mode
	for _, m := range members {
		if m.applicable&timeseries.StepAlignmentTruncate == 0 {
			return 0
		}
	}
	return timeseries.StepAlignmentTruncate
}

func memberNames(o *ao.Options) []string {
	// the pool in configured order, then a user router's targets
	if o == nil {
		return nil
	}
	out := make([]string, 0, len(o.Pool))
	seen := sets.NewStringSet()
	add := func(name string) {
		if name != "" && !seen.Contains(name) {
			seen.Set(name)
			out = append(out, name)
		}
	}
	for _, m := range o.Pool {
		add(m.Name)
	}
	if o.UserRouter == nil {
		return out
	}
	add(o.UserRouter.DefaultBackend)
	users := make([]string, 0, len(o.UserRouter.Users))
	for _, u := range o.UserRouter.Users {
		if u != nil {
			users = append(users, u.ToBackend)
		}
	}
	slices.Sort(users)
	for _, name := range users {
		add(name)
	}
	return out
}

func alignmentsOf(members []string, clients backends.Backends,
	visited sets.Set[string],
) []memberAlignment {
	out := make([]memberAlignment, 0, len(members))
	for _, name := range members {
		out = append(out, alignmentOf(name, clients, visited))
	}
	return out
}

func alignmentOf(name string, clients backends.Backends, visited sets.Set[string]) memberAlignment {
	out := memberAlignment{name: name}
	b := clients[name]
	if b == nil || b.Configuration() == nil {
		return out
	}
	o := b.Configuration()
	switch {
	case o.Provider == providers.Rule, visited.Contains(name):
		// a rule's routes are chosen per request, and a pool cycle is reported by pool validation
		out.applicable = timeseries.StepAlignmentAll
	case o.Provider == providers.ALB:
		// members answer under the outermost ALB's mode, so a nested ALB applies whatever its
		// members all apply; its own mode, else its leader's, is what it would choose
		next := visited.Clone()
		next.Set(name)
		nested := alignmentsOf(memberNames(o.ALBOptions), clients, next)
		out.effective, out.applicable = o.StepAlignment, timeseries.StepAlignmentAll
		if out.effective == 0 && len(nested) > 0 {
			out.effective = nested[0].effective
		}
		for _, m := range nested {
			out.applicable &= m.applicable
		}
	default:
		out.effective, out.applicable = backends.StepAlignmentProfile(b)
	}
	return out
}

func (c *Client) validateStepAlignment(clients backends.Backends) error {
	// a member that can't apply the ALB's mode fails the configuration; one discovered later falls back
	o := c.Configuration()
	if !appliesStepAlignment(o) {
		return nil
	}
	members := alignmentsOf(memberNames(o.ALBOptions), clients, sets.New([]string{c.Name()}))
	mode, lacking := resolveStepAlignment(o.StepAlignment, followsLeader(o.ALBOptions), members)
	if len(lacking) > 0 {
		return bo.NewErrStepAlignmentUnsupportedByMembers(mode, c.Name(), lacking)
	}
	return nil
}

// StepAlignmentWarnings returns a warning for each ALB whose members apply different step alignment
// modes, sorted by the ALB's name
func StepAlignmentWarnings(clients backends.Backends) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(clients)) {
		if c, ok := clients[name].(*Client); ok {
			if w := c.mixedStepAlignmentWarning(clients); w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

func (c *Client) mixedStepAlignmentWarning(clients backends.Backends) string {
	o := c.Configuration()
	applies := appliesStepAlignment(o)
	if o == nil || o.ALBOptions == nil || (!applies && o.ALBOptions.MechanismName == names.MechanismUR) {
		// a user router sends each user to one backend, so its targets differing is by design
		return ""
	}
	members := alignmentsOf(memberNames(o.ALBOptions), clients, sets.New([]string{c.Name()}))
	var modes timeseries.StepAlignment
	for _, m := range members {
		modes |= m.effective
	}
	if modes == 0 || modes.IsMode() {
		return ""
	}
	if !applies {
		return fmt.Sprintf("alb %q pool members apply different step alignment modes (%s), and each "+
			"answers with its own; set step_alignment on the alb to apply one", c.Name(), memberModes(members))
	}
	mode, _ := resolveStepAlignment(o.StepAlignment, followsLeader(o.ALBOptions), members)
	return fmt.Sprintf("alb %q pool members apply different step alignment modes (%s); the alb has "+
		"all of them apply %s", c.Name(), memberModes(members), mode)
}

func memberModes(members []memberAlignment) string {
	var b strings.Builder
	for i, m := range members {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(m.name)
		b.WriteByte('=')
		if m.effective == 0 {
			b.WriteString(noStepAlignment)
			continue
		}
		b.WriteString(m.effective.String())
	}
	return b.String()
}

func (c *Client) alignPool(targets pool.Targets) pool.Alignment {
	// resolves the alignment of the pool about to be built from targets, falling back when a
	// discovered member can't apply the ALB's choice; callers hold c.poolMtx
	o := c.Configuration()
	if !appliesStepAlignment(o) {
		return pool.Alignment{}
	}
	members := make([]memberAlignment, 0, len(targets))
	for _, t := range targets {
		if t != nil {
			members = append(members, c.targetAlignment(t))
		}
	}
	mode, lacking := resolveStepAlignment(o.StepAlignment, followsLeader(o.ALBOptions), members)
	var warning string
	if len(lacking) > 0 {
		chosen := mode
		mode = fallbackStepAlignment(members)
		outcome := "every member uses " + mode.String()
		if mode == 0 {
			outcome = "each member uses its own and results may be misaligned"
		}
		warning = fmt.Sprintf("trickster: pool members [%s] can't apply step alignment %s, so %s",
			strings.Join(lacking, ", "), chosen, outcome)
	}
	if warning != "" && warning != c.alignmentWarning {
		logger.Warn("alb pool step alignment fallback", logging.Pairs{
			keys.BackendName: c.Name(), keys.Detail: warning,
		})
	}
	c.alignmentWarning = warning
	return pool.Alignment{Mode: mode, Allowed: allowedStepAlignments(members), Warning: warning}
}

func (c *Client) targetAlignment(t *pool.Target) memberAlignment {
	if a, ok := c.staticAlignments[t.Name()]; ok {
		return a
	}
	out := memberAlignment{name: t.Name()}
	out.effective, out.applicable = backends.StepAlignmentProfile(t.Backend())
	return out
}

func (c *Client) serveRouted(w http.ResponseWriter, r *http.Request) {
	// a user router has no pool, so the mode set on it, fixed at load, is the one its targets answer under
	c.handler.ServeHTTP(w, mech.Align(r, c.routerOverride.Load()))
}
