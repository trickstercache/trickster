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

package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemberKeyAndURL(t *testing.T) {
	m := &Member{Name: "pod-1", Address: "10.0.0.1:9090"}
	require.Equal(t, "http://10.0.0.1:9090", m.URL(), "scheme should default to http")
	m.Scheme = "https"
	m.PathPrefix = "/prom"
	require.Equal(t, "https://10.0.0.1:9090/prom", m.URL())
	require.Equal(t, m.URL(), m.Key())
}

func TestMemberEqualAndClone(t *testing.T) {
	m := Member{Name: "a", Scheme: "http", Address: "h:1", Weight: 2,
		Ready: Ready, Labels: map[string]string{"k": "v"}}
	c := m.Clone()
	require.True(t, m.Equal(&c))
	c.Labels["k"] = "other"
	require.False(t, m.Equal(&c), "label changes must not alias the original")
	require.Equal(t, "v", m.Labels["k"])
	c = m.Clone()
	c.Ready = Terminating
	require.False(t, m.Equal(&c))
}

func TestReadyStateString(t *testing.T) {
	require.Equal(t, "ready", Ready.String())
	require.Equal(t, "terminating", Terminating.String())
	require.Equal(t, "unknown", ReadyState(42).String())
}

func TestSnapshotCanonical(t *testing.T) {
	s := Snapshot{
		{Name: "b", Address: "10.0.0.2:80"},
		{Name: "a", Address: "10.0.0.1:80"},
		{Name: "a-dupe", Address: "10.0.0.1:80"}, // same Key as "a"
	}
	c := s.Canonical()
	require.Len(t, c, 2)
	require.Equal(t, "a", c[0].Name, "sorted by key; first-in-order wins dedupe")
	require.Equal(t, "b", c[1].Name)
	// input snapshot is not mutated
	require.Len(t, s, 3)
}

func TestSnapshotEqual(t *testing.T) {
	a := Snapshot{
		{Name: "a", Address: "10.0.0.1:80"},
		{Name: "b", Address: "10.0.0.2:80"},
	}
	b := Snapshot{
		{Name: "b", Address: "10.0.0.2:80"},
		{Name: "a", Address: "10.0.0.1:80"},
	}
	require.True(t, a.Equal(b), "order must not affect equality")
	b[0].Weight = 5
	require.False(t, a.Equal(b))
	require.False(t, a.Equal(a[:1]))
}

func TestSnapshotBackendNames(t *testing.T) {
	s := Snapshot{
		{Name: "Pod A", Address: "10.0.0.1:80"},
		{Name: "pod-b", Address: "10.0.0.2:80"},
		{Address: "10.0.0.3:80"}, // unnamed: seeded from address
	}
	names := s.BackendNames("my-alb")
	require.Len(t, names, 3)
	require.Contains(t, names, "my-alb-pod-a")
	require.Contains(t, names, "my-alb-pod-b")
	require.Contains(t, names, "my-alb-10.0.0.3-80")
}

func TestSnapshotBackendNamesCollision(t *testing.T) {
	s := Snapshot{
		{Name: "pod a", Address: "10.0.0.1:80"}, // sanitizes to pod-a
		{Name: "pod-a", Address: "10.0.0.2:80"},
	}
	names := s.BackendNames("alb")
	require.Len(t, names, 2)
	// deterministic regardless of input order
	s2 := Snapshot{s[1], s[0]}
	names2 := s2.BackendNames("alb")
	require.Equal(t, names, names2)
}

func TestMemberReplicaGroupEquality(t *testing.T) {
	a := Member{Name: "m1", Address: "10.0.0.1:80", ReplicaGroup: "shard-0"}
	b := a.Clone()
	require.True(t, a.Equal(&b))
	b.ReplicaGroup = "shard-1"
	require.False(t, a.Equal(&b),
		"a replica-group change must register as a member change")
}
