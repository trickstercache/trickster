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

package sticky

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/secret"
)

const (
	albName = "app"
	born    = int64(1_800_000_000)
	issued  = born + 600
)

var (
	key      = []byte(strings.Repeat("k", secret.MinKeyBytes))
	otherKey = []byte(strings.Repeat("o", secret.MinKeyBytes))
	leaf     = Path{Hashes: [lb.MaxPickDepth]uint64{0x0123456789abcdef}, Depth: 1}
	nested   = Path{Hashes: [lb.MaxPickDepth]uint64{0x0123456789abcdef, 0xfedcba9876543210}, Depth: 2}
)

func codec(t testing.TB, albName string, ttl, idle time.Duration, keys ...[]byte) *Codec {
	t.Helper()
	if len(keys) == 0 {
		keys = [][]byte{key}
	}
	k, err := secret.NewKeyring(keys...)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCodec(k, albName, ttl, idle)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A token is pinned for a fixed key, ALB and payload: replicas must agree on it, and a change to
// its format or key must be a new version, not a silent end to every session.
func TestTokenGolden(t *testing.T) {
	c := codec(t, albName, 0, 0)
	for _, tc := range []struct {
		path Path
		want string
	}{
		{leaf, "AQFrSdIAa0nUWAEjRWeJq83vy-dURVUAMO0p7ajAx9AgaQ"},
		{nested, "AQJrSdIAa0nUWAEjRWeJq83v_ty6mHZUMhA0lgzxXuke-L57AgGOGfLm"},
	} {
		tok := Token{Path: tc.path, Born: born, Issued: issued}
		if got := c.Mint(tok); got != tc.want {
			t.Errorf("depth %d minted %s, want %s", tc.path.Depth, got, tc.want)
		}
		if got, st := c.Read(tc.want, issued); st != Valid || got != tok {
			t.Errorf("depth %d read %+v (%v)", tc.path.Depth, got, st)
		}
	}
}

func TestTokenRejectsWhatItDidNotIssue(t *testing.T) {
	c := codec(t, albName, 0, 0)
	tok := c.Mint(Token{Path: nested, Born: born, Issued: issued})
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for i := range len(tok) {
		for _, r := range []byte{'A', 'B'} {
			if tok[i] == r {
				continue
			}
			altered := tok[:i] + string(r) + tok[i+1:]
			if _, st := c.Read(altered, issued); st != Invalid {
				t.Fatalf("a token altered at %d read as %v", i, st)
			}
		}
	}
	for i := range len(tok) {
		if _, st := c.Read(tok[:i], issued); st != Invalid {
			t.Fatalf("a token truncated to %d read as %v", i, st)
		}
	}
	for _, s := range []string{tok + "A", tok + "AA", tok + "=", strings.Repeat(alphabet, 4), "not a token!"} {
		if _, st := c.Read(s, issued); st != Invalid {
			t.Errorf("%q read as %v", s, st)
		}
	}
	if _, st := codec(t, "other", 0, 0).Read(tok, issued); st != Invalid {
		t.Error("another ALB honored the token")
	}
	if _, st := codec(t, albName, 0, 0, otherKey).Read(tok, issued); st != Invalid {
		t.Error("a codec with another key honored the token")
	}
	// the name is bound length first, so no split of it between name and token lines up
	if _, st := codec(t, albName+"\x01", 0, 0).Read(tok, issued); st != Invalid {
		t.Error("a longer ALB name honored the token")
	}
}

// a token with a sound tag but a body this version cannot hold is still refused
func TestTokenRejectsASignedBodyItCannotHold(t *testing.T) {
	c := codec(t, albName, 0, 0)
	sign := func(version, depth byte, hashes int) string {
		raw := []byte{version, depth}
		raw = binary.BigEndian.AppendUint32(raw, uint32(born))
		raw = binary.BigEndian.AppendUint32(raw, uint32(issued))
		for range hashes {
			raw = binary.BigEndian.AppendUint64(raw, 42)
		}
		return encoding.EncodeToString(c.signer.Sum(raw, c.bound, raw))
	}
	if _, st := c.Read(sign(tokenVersion, 1, 1), issued); st != Valid {
		t.Fatal("the control token was refused")
	}
	for name, tok := range map[string]string{
		"version 2":            sign(tokenVersion+1, 1, 1),
		"depth 0":              sign(tokenVersion, 0, 0),
		"depth 3":              sign(tokenVersion, lb.MaxPickDepth+1, lb.MaxPickDepth+1),
		"depth 2 with 1 level": sign(tokenVersion, 2, 1),
		"depth 1 with 2":       sign(tokenVersion, 1, 2),
	} {
		if _, st := c.Read(tok, issued); st != Invalid {
			t.Errorf("%s read as %v", name, st)
		}
	}
}

func TestTokenMintNeedsALevel(t *testing.T) {
	c := codec(t, albName, 0, 0)
	for _, depth := range []uint8{0, lb.MaxPickDepth + 1} {
		if s := c.Mint(Token{Path: Path{Depth: depth}}); s != "" {
			t.Errorf("depth %d minted %q", depth, s)
		}
	}
}

func TestTokenTTL(t *testing.T) {
	c := codec(t, albName, time.Hour, 0)
	tok := c.Mint(Token{Path: leaf, Born: born, Issued: issued})
	if _, st := c.Read(tok, born+3599); st != Valid {
		t.Errorf("read as %v before its ttl", st)
	}
	got, st := c.Read(tok, born+3600)
	if st != Expired {
		t.Errorf("read as %v at its ttl", st)
	}
	if got.Path != leaf {
		t.Error("an expired token did not report its path")
	}
	if exp := c.Expires(got); exp != born+3600 {
		t.Errorf("Expires = %d", exp-born)
	}
	// reissuing does not extend a ttl: it runs from when the session was first pinned
	if _, st := c.Read(c.Mint(Token{Path: leaf, Born: born, Issued: born + 3599}), born+3600); st != Expired {
		t.Error("a reissued token outlived its session's ttl")
	}
}

func TestTokenIdle(t *testing.T) {
	c := codec(t, albName, 0, 10*time.Minute)
	tok := c.Mint(Token{Path: leaf, Born: born, Issued: issued})
	if _, st := c.Read(tok, issued+599); st != Valid {
		t.Errorf("read as %v before its idle timeout", st)
	}
	if _, st := c.Read(tok, issued+600); st != Expired {
		t.Errorf("read as %v at its idle timeout", st)
	}
	if exp := c.Expires(Token{Born: born, Issued: issued}); exp != issued+600 {
		t.Errorf("Expires = %d after issue", exp-issued)
	}
	if exp := codec(t, albName, 0, 0).Expires(Token{Born: born, Issued: issued}); exp != 0 {
		t.Errorf("with no limits Expires = %d", exp)
	}
}

func TestTokenTTLAndIdle(t *testing.T) {
	c := codec(t, albName, time.Hour, 10*time.Minute)
	for _, tc := range []struct{ issued, expires int64 }{
		{born, born + 600},
		{born + 2000, born + 2600},
		// the ttl comes first
		{born + 3300, born + 3600},
	} {
		if exp := c.Expires(Token{Born: born, Issued: tc.issued}); exp != tc.expires {
			t.Errorf("issued at %d: Expires = %d, want %d", tc.issued-born, exp-born, tc.expires-born)
		}
	}
}

func TestTokenRefreshDue(t *testing.T) {
	idle := codec(t, albName, 0, 10*time.Minute)
	both := codec(t, albName, time.Hour, 10*time.Minute)
	for _, tc := range []struct {
		c      *Codec
		issued int64
		now    int64
		want   bool
	}{
		{codec(t, albName, time.Hour, 0), born, born + 3500, false},
		{idle, issued, issued + 300, false},
		{idle, issued, issued + 301, true},
		{idle, issued, issued + 599, true},
		{both, born, born + 301, true},
		// a reissued token would expire no later, at the session's ttl
		{both, born + 3000, born + 3301, false},
		{both, born + 2999, born + 3300, true},
	} {
		if got := tc.c.RefreshDue(Token{Born: born, Issued: tc.issued}, tc.now); got != tc.want {
			t.Errorf("issued at %d, now %d: RefreshDue = %v", tc.issued-born, tc.now-born, got)
		}
	}
}

// a limit shorter than a second is not taken for no limit
func TestTokenRoundsLimitsUp(t *testing.T) {
	c := codec(t, albName, 1500*time.Millisecond, 0)
	if exp := c.Expires(Token{Born: born}); exp != born+2 {
		t.Errorf("a 1.5s ttl expires %ds after the session began", exp-born)
	}
	if exp := codec(t, albName, time.Millisecond, 0).Expires(Token{Born: born}); exp != born+1 {
		t.Errorf("a 1ms ttl expires %ds after the session began", exp-born)
	}
}

// times outside what four bytes hold are clamped, not wrapped into another time
func TestTokenClampsTimes(t *testing.T) {
	c := codec(t, albName, 0, 0)
	got, st := c.Read(c.Mint(Token{Path: leaf, Born: -5, Issued: math.MaxUint32 + 10}), 0)
	if st != Valid || got.Born != 0 || got.Issued != math.MaxUint32 {
		t.Errorf("read %+v (%v)", got, st)
	}
}

func TestTokenRotation(t *testing.T) {
	fromOld := codec(t, albName, 0, 0, otherKey).Mint(Token{Path: leaf, Born: born, Issued: issued})
	rotated := codec(t, albName, 0, 0, key, otherKey)
	if _, st := rotated.Read(fromOld, issued); st != Valid {
		t.Errorf("a token from the previous key read as %v", st)
	}
	fresh := rotated.Mint(Token{Path: leaf, Born: born, Issued: issued})
	if _, st := codec(t, albName, 0, 0).Read(fresh, issued); st != Valid {
		t.Error("tokens are not minted with the newest key")
	}
}

func TestTokenConcurrentUse(t *testing.T) {
	c := codec(t, albName, time.Hour, 0)
	want := Token{Path: nested, Born: born, Issued: issued}
	tok := c.Mint(want)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				if got, st := c.Read(c.Mint(want), issued); st != Valid || got != want {
					t.Error("concurrent use changed a token")
					return
				}
				if _, st := c.Read(tok, issued); st != Valid {
					t.Error("concurrent use refused a token")
					return
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkTokenRead(b *testing.B) {
	c := codec(b, albName, time.Hour, 10*time.Minute)
	for _, p := range []Path{leaf, nested} {
		tok := c.Mint(Token{Path: p, Born: born, Issued: issued})
		b.Run("depth="+string(rune('0'+p.Depth)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c.Read(tok, issued)
			}
		})
	}
}

func BenchmarkTokenMint(b *testing.B) {
	c := codec(b, albName, time.Hour, 10*time.Minute)
	tok := Token{Path: nested, Born: born, Issued: issued}
	b.ReportAllocs()
	for b.Loop() {
		c.Mint(tok)
	}
}
