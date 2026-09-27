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

package gateway

import (
	"fmt"
	"strconv"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

// sessionSite is the host a rule's generated ALBs serve, or empty for any; a browser shares a
// host's cookies across every port and scheme, so the listener does not tell them apart
type sessionSite struct {
	host string
}

// cookieClaim is a named session cookie that an ALB sets for the hosts it serves; none is any host
type cookieClaim struct {
	owner string
	hosts []string
}

// ruleSession returns the session a rule keeps, or nil when its ALBs cannot keep one: a rule with
// one backendRef has an ALB only in the endpoint routing mode, where Trickster picks the endpoint
func (t *translator) ruleSession(src ir.Source, rule int, s *ir.Session, g ir.BackendGroup,
	mode string, at sessionSite,
) *ir.Session {
	if s == nil || len(g.Members) == 0 || (len(g.Members) == 1 && g.Members[0].Invalid) {
		return nil
	}
	if mode == kubecfg.RoutingModeService {
		if len(g.Members) == 1 {
			t.reject(src, "rule %d: sessionPersistence on a rule with one backendRef needs the "+
				"endpoint routing mode; sessions are not kept", rule)
			return nil
		}
		t.reject(src, "rule %d: sessionPersistence in the service routing mode keeps a session "+
			"on its Service, and kube-proxy chooses the endpoint", rule)
	}
	if other, ok := t.claimCookie(s, at, src.Key()+" rule "+strconv.Itoa(rule)); !ok {
		t.reject(src, "rule %d: %s; sessions are not kept", rule, cookieTaken(s, other))
		return nil
	}
	return s
}

// memberSessions keeps the sessions the members' Services ask for where each member's ALB can
// keep one; a session of the rule's own carries every member, so theirs are dropped
func (t *translator) memberSessions(src ir.Source, g *ir.BackendGroup, ruled bool, mode string,
	at sessionSite,
) {
	for i := range g.Members {
		m := &g.Members[i]
		if m.Session == nil {
			continue
		}
		if ruled || mode == kubecfg.RoutingModeService {
			// a Service reached by its cluster IP has its endpoint chosen by kube-proxy
			m.Session = nil
			continue
		}
		owner := src.Key() + " backendRef " + strconv.Itoa(m.RefIndex)
		if other, ok := t.claimCookie(m.Session, at, owner); !ok {
			t.reject(src, "backendRef %d: %s; sessions are not kept", m.RefIndex,
				cookieTaken(m.Session, other))
			m.Session = nil
		}
	}
}

// claimCookie claims a named session cookie for the site's host on every listener, returning the
// owner already holding it for a shared host; unnamed cookies never collide
func (t *translator) claimCookie(s *ir.Session, at sessionSite, owner string) (string, bool) {
	if s.Type != ir.SessionCookie || s.Name == "" {
		return "", true
	}
	claim := cookieClaim{owner: owner}
	if at.host != "" {
		claim.hosts = []string{hostnames.ToAnyDepth(at.host)}
	}
	if t.cookies == nil {
		t.cookies = make(map[string][]cookieClaim)
	}
	for _, c := range t.cookies[s.Name] {
		if hostnames.ListsOverlap(c.hosts, claim.hosts) {
			return c.owner, false
		}
	}
	t.cookies[s.Name] = append(t.cookies[s.Name], claim)
	return "", true
}

func cookieTaken(s *ir.Session, owner string) string {
	return fmt.Sprintf("session cookie %q is already set for this host by %s, on this or another "+
		"port", s.Name, owner)
}
