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

package integration

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"

	"github.com/stretchr/testify/require"
)

const (
	nestedDiscoveryInnerA = "inner-a"
	nestedDiscoveryInnerB = "inner-b"
	nestedDiscoveryOuter  = "outer"
	nestedDiscoveryRounds = 10
	nestedDiscoveryWeight = 3
)

// nestedDiscoveryConfig renders a weighted round-robin outer ALB whose pool
// members are two discovery-backed inner ALBs, each instantiating members
// from a shared template and trusting the provider's readiness signal. This
// is the topology a gateway controller emits for weighted backendRefs.
func nestedDiscoveryConfig(frontPort, metricsPort, mgmtPort int, membersA, membersB string) string {
	return fmt.Sprintf(`
listeners:
  default:
    port: %[1]d
  metrics:
    port: %[2]d
  mgmt:
    port: %[3]d
logging:
  log_level: info
discovery:
  d-a:
    provider: file
  d-b:
    provider: file
backends:
  member-template:
    provider: rp
    is_template: true
  %[4]s:
    provider: alb
    alb:
      mechanism: rr
      discovery:
        discoverer_name: d-a
        template_backend: member-template
        health_mode: provider
        query:
          path: %[6]s
  %[5]s:
    provider: alb
    alb:
      mechanism: rr
      discovery:
        discoverer_name: d-b
        template_backend: member-template
        health_mode: provider
        query:
          path: %[7]s
  %[8]s:
    provider: alb
    is_default: true
    alb:
      mechanism: rr
      pool:
        - name: %[4]s
          weight: %[9]d
        - name: %[5]s
          weight: 1
`, frontPort, metricsPort, mgmtPort, nestedDiscoveryInnerA, nestedDiscoveryInnerB,
		membersA, membersB, nestedDiscoveryOuter, nestedDiscoveryWeight)
}

func TestALBNestedDiscoveryWeightedOuter(t *testing.T) {
	ports, release := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]

	leafA1 := newDiscoveryLeaf(t, "leafA1")
	leafA2 := newDiscoveryLeaf(t, "leafA2")
	leafB1 := newDiscoveryLeaf(t, "leafB1")
	dir := t.TempDir()
	membersA := filepath.Join(dir, "members-a.yaml")
	membersB := filepath.Join(dir, "members-b.yaml")
	writeMembersFile(t, membersA, leafA1, leafA2)
	writeMembersFile(t, membersB, leafB1)

	release()
	startDiscoveryTrickster(t, nestedDiscoveryConfig(frontPort, metricsPort, mgmtPort, membersA, membersB))
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, nestedDiscoveryInnerA, 2)
	waitDiscoveredMembers(t, metricsAddr, nestedDiscoveryInnerB, 1)

	healthURL := "http://" + metricsAddr + "/trickster/health"
	requireALBMemberState(t, healthURL, nestedDiscoveryOuter, nestedDiscoveryInnerA, "available", 10*time.Second)
	requireALBMemberState(t, healthURL, nestedDiscoveryOuter, nestedDiscoveryInnerB, "available", 10*time.Second)

	// weighted round robin over a stable healthy set apportions exactly, so
	// ten full cycles of the 3:1 pool split 30:10 between the inner ALBs.
	// Count the responses to this request set instead of the leaves' lifetime
	// hit counters: suite-level lifecycle traffic may also reach an upstream,
	// but it is not part of the distribution this assertion exercises.
	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	total := nestedDiscoveryRounds * (nestedDiscoveryWeight + 1)
	leafHits := make(map[string]int, 3)
	for range total {
		status, body := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status)
		leafHits[body]++
	}
	groupA := leafHits[leafA1.name] + leafHits[leafA2.name]
	groupB := leafHits[leafB1.name]
	require.Equal(t, nestedDiscoveryRounds*nestedDiscoveryWeight, groupA,
		"inner-a share of traffic")
	require.Equal(t, nestedDiscoveryRounds, groupB, "inner-b share of traffic")
	require.Positive(t, leafHits[leafA1.name], "inner-a must spread across its discovered members")
	require.Positive(t, leafHits[leafA2.name], "inner-a must spread across its discovered members")
}
