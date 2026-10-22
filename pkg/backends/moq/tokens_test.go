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

package moq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func tokens(values ...string) []wire.Token {
	out := make([]wire.Token, len(values))
	for i, v := range values {
		out[i] = wire.Token{AliasType: wire.TokenUseValue, Value: []byte(v)}
	}
	return out
}

func namespace(s string) wire.Namespace {
	return wire.Namespace(func() [][]byte {
		var out [][]byte
		for f := range strings.SplitSeq(s, "/") {
			out = append(out, []byte(f))
		}
		return out
	}())
}

func writeTokens(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTokenGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokens(t, path, "# grants\n\nstudio-key publish live/studio\nviewer subscribe\nops ANY live\n")
	tf, err := newTokenFile("live", path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		op    relay.Operation
		ns    string
		token []wire.Token
		want  bool
	}{
		{relay.OpPublish, "live/studio/cam", tokens("studio-key"), true},
		{relay.OpPublish, "live/other", tokens("studio-key"), false},
		{relay.OpSubscribe, "live/studio", tokens("studio-key"), false},
		{relay.OpSubscribe, "anything", tokens("viewer"), true},
		{relay.OpPublish, "live/x", tokens("ops"), true},
		{relay.OpSubscribe, "live/x", tokens("unknown", "ops"), true},
		{relay.OpSubscribe, "vod", tokens("ops"), false},
		{relay.OpSubscribe, "live", nil, false},
	}
	for _, tc := range tests {
		if got := tf.Authorize(tc.op, namespace(tc.ns), tc.token); got != tc.want {
			t.Errorf("op %d on %s with %v: got %t", tc.op, tc.ns, tc.token, got)
		}
	}
}

func TestTokenFileReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokens(t, path, "first subscribe\n")
	tf, err := newTokenFile("live", path)
	if err != nil {
		t.Fatal(err)
	}
	if !tf.Authorize(relay.OpSubscribe, nil, tokens("first")) {
		t.Fatal("the first grant")
	}
	// a change is noticed at the next check; the recheck interval is passed by rewinding the last check
	writeTokens(t, path, "second-token subscribe\n")
	tf.mu.Lock()
	tf.checked = time.Time{}
	tf.mu.Unlock()
	if tf.Authorize(relay.OpSubscribe, nil, tokens("first")) || !tf.Authorize(relay.OpSubscribe, nil, tokens("second-token")) {
		t.Error("a changed file replaces the grants")
	}
	// a file that no longer parses keeps the grants it had
	writeTokens(t, path, "broken line with too many fields\n")
	tf.mu.Lock()
	tf.checked = time.Time{}
	tf.mu.Unlock()
	if !tf.Authorize(relay.OpSubscribe, nil, tokens("second-token")) {
		t.Error("an unparsable file keeps the last grants")
	}
}

func TestTokenFileErrors(t *testing.T) {
	if _, err := newTokenFile("live", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file")
	}
	for _, content := range []string{"lonely\n", "t write\n", "t publish live//x\n", "a b c d\n"} {
		if _, err := parseTokens([]byte(content)); err == nil {
			t.Errorf("%q should not parse", content)
		}
	}
}
