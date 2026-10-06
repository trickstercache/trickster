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

package ratelimit

import (
	"testing"
	"time"
)

func TestLookupReusesCountsWhenOnlyPolicyChanges(t *testing.T) {
	resetRegistry()
	shape := Shape{Keys: EncodeKeys([]string{"client_ip"}), Window: time.Second, Limit: 2, MaxKeys: 10}
	first := Lookup("edge", shape, Policy{Missing: MissingExempt})
	if !first.Take(1, true, 0, 1).Allowed || !first.Take(1, true, 0, 1).Allowed {
		t.Fatal("setup")
	}
	second := Lookup("edge", shape, Policy{Missing: MissingShared, OnFull: MaxKeysReject})
	if second.Take(1, true, 0, 1).Allowed {
		t.Fatal("a policy change started a fresh table")
	}
	if second.missing != MissingShared || second.onFull != MaxKeysReject {
		t.Fatal("the new policy was not applied")
	}
	if second.store != first.store {
		t.Fatal("the snapshots do not share a store")
	}
}

func TestLookupDropsCountsWhenTheShapeChanges(t *testing.T) {
	base := Shape{
		Keys: EncodeKeys([]string{"client_ip"}), Window: time.Second, Limit: 1, MaxKeys: 10, IPv6Prefix: 64,
	}
	edits := []struct {
		name string
		edit func(*Shape)
	}{
		{"limit", func(s *Shape) { s.Limit = 2 }},
		{"window", func(s *Shape) { s.Window = 2 * time.Second }},
		{"max keys", func(s *Shape) { s.MaxKeys = 11 }},
		{"ipv6 prefix", func(s *Shape) { s.IPv6Prefix = 48 }},
		{"keys", func(s *Shape) { s.Keys = EncodeKeys([]string{"path"}) }},
	}
	for _, tc := range edits {
		t.Run(tc.name, func(t *testing.T) {
			resetRegistry()
			shape := base
			Lookup("edge", shape, Policy{}).Take(1, true, 0, 1)
			tc.edit(&shape)
			if !Lookup("edge", shape, Policy{}).Take(1, true, 0, 1).Allowed {
				t.Fatal("a shape change kept the old count")
			}
		})
	}
}

func TestHeldLimiterSurvivesForget(t *testing.T) {
	resetRegistry()
	shape := Shape{Window: time.Second, Limit: 1, MaxKeys: 10}
	held := Lookup("gone", shape, Policy{})
	if !held.Take(1, true, 0, 1).Allowed {
		t.Fatal("setup")
	}
	ForgetExcept(func(string) bool { return false })
	if held.Take(1, true, 0, 1).Allowed {
		t.Fatal("forgetting the name dropped the in-flight store")
	}
	if !Lookup("gone", shape, Policy{}).Take(1, true, 0, 1).Allowed {
		t.Fatal("a new lookup reused the forgotten store")
	}
}

func TestFailedReloadLeavesTheRegistry(t *testing.T) {
	resetRegistry()
	shape := Shape{Window: time.Second, Limit: 1, MaxKeys: 10}
	Lookup("keep", shape, Policy{}).Take(1, true, 0, 1)
	applied := false
	if applied {
		ForgetExcept(func(name string) bool { return name == "keep" })
	}
	if Lookup("keep", shape, Policy{}).Take(1, true, 0, 1).Allowed {
		t.Fatal("a reload that did not apply forgot the count")
	}
	ForgetExcept(func(name string) bool { return false })
	if !Lookup("keep", shape, Policy{}).Take(1, true, 0, 1).Allowed {
		t.Fatal("forgetting the name did not start fresh")
	}
}

func TestForgetExceptKeepsNamedStores(t *testing.T) {
	resetRegistry()
	shape := Shape{Window: time.Second, Limit: 1, MaxKeys: 10}
	Lookup("keep", shape, Policy{}).Take(1, true, 0, 1)
	Lookup("drop", shape, Policy{}).Take(1, true, 0, 1)
	ForgetExcept(func(name string) bool { return name == "keep" })
	if Lookup("keep", shape, Policy{}).Take(1, true, 0, 1).Allowed {
		t.Fatal("the kept name lost its count")
	}
	if !Lookup("drop", shape, Policy{}).Take(1, true, 0, 1).Allowed {
		t.Fatal("the dropped name kept its count")
	}
}

func TestEncodeKeysPreservesOrder(t *testing.T) {
	if EncodeKeys([]string{"a", "b"}) == EncodeKeys([]string{"b", "a"}) {
		t.Fatal("key order did not change the identity")
	}
	if EncodeKeys(nil) != "" {
		t.Fatal("no keys should be the one-bucket identity")
	}
}
