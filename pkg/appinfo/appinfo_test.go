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

package appinfo

import (
	"os"
	"testing"
)

func TestServerDefault(t *testing.T) {
	hn, _ := os.Hostname()
	if got := Server(); got != hn {
		t.Errorf("expected default server %q, got %q", hn, got)
	}
}

func TestServerNil(t *testing.T) {
	prev := server.Load()
	t.Cleanup(func() {
		server.Store(prev)
	})

	server.Store(nil)
	if got := Server(); got != "" {
		t.Errorf("expected empty server, got %q", got)
	}
}

func TestSetServer(t *testing.T) {
	prev := server.Load()
	t.Cleanup(func() {
		server.Store(prev)
	})

	const want = "trickster-test-host"
	SetServer(want)
	if got := Server(); got != want {
		t.Errorf("expected server %q, got %q", want, got)
	}
}

func TestSet(t *testing.T) {
	prevName, prevVersion := Name, Version
	prevBuildTime, prevGitCommitID := BuildTime, GitCommitID
	t.Cleanup(func() {
		Name, Version = prevName, prevVersion
		BuildTime, GitCommitID = prevBuildTime, prevGitCommitID
	})

	Set("trickster", "2.0.0", "2026-01-01T00:00:00Z", "abc123")
	if Name != "trickster" {
		t.Errorf("expected Name %q, got %q", "trickster", Name)
	}
	if Version != "2.0.0" {
		t.Errorf("expected Version %q, got %q", "2.0.0", Version)
	}
	if BuildTime != "2026-01-01T00:00:00Z" {
		t.Errorf("expected BuildTime %q, got %q", "2026-01-01T00:00:00Z", BuildTime)
	}
	if GitCommitID != "abc123" {
		t.Errorf("expected GitCommitID %q, got %q", "abc123", GitCommitID)
	}
}
