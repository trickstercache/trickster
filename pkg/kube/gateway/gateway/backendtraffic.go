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
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

// trafficSession is the session a traffic policy asks for, and the policy that asked
type trafficSession struct {
	session *ir.Session
	src     ir.Source
}

// indexBackendTraffic maps each Service, by namespace/name, to the session persistence the oldest
// XBackendTrafficPolicy targeting it asks for; a policy that cannot be honored governs nothing
func (t *translator) indexBackendTraffic() map[string]trafficSession {
	out := make(map[string]trafficSession)
	for _, p := range translate.ByAge(t.cfg.Cache.BackendTrafficPolicies()) {
		src := translate.Source(ir.KindBackendTrafficPolicy, p)
		if p.Spec.RetryConstraint != nil {
			t.reject(src, "retryConstraint is not supported and is ignored")
		}
		if p.Spec.SessionPersistence == nil {
			continue
		}
		session, err := lowerSession(p.Spec.SessionPersistence)
		if err != nil {
			t.reject(src, "%s; sessions are not kept", err)
			continue
		}
		for _, ref := range p.Spec.TargetRefs {
			if string(ref.Group) != "" || string(ref.Kind) != kindService {
				t.reject(src, "targetRef kind %s/%s is not supported", ref.Group, ref.Kind)
				continue
			}
			key := p.Namespace + "/" + string(ref.Name)
			if owner, taken := out[key]; taken {
				t.reject(src, "targetRef %s is already selected by the older policy %s", key,
					owner.src.Key())
				continue
			}
			out[key] = trafficSession{session: session, src: src}
		}
	}
	return out
}

// serviceSession returns a copy of the session a traffic policy asks for on the Service, or nil
func (t *translator) serviceSession(namespace, service string) *ir.Session {
	return t.traffic[namespace+"/"+service].session.Clone()
}
