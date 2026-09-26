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

// Package sticky keeps a client on the ALB pool member it was first sent to, by a keyed token the
// client sends back or by a table of pins keyed on each request or flow.
package sticky

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/secret"
)

// Purpose is the label that the key sticky tokens are made with is derived under.
const Purpose = "trickster/alb-sticky/v1"

// Path is the member a session is pinned to at each level of a nested pick, by member hash.
type Path struct {
	Hashes [lb.MaxPickDepth]uint64
	Depth  uint8
}

// Token is what a sticky token carries: the path, when the session was first pinned, and when
// this token was issued, both in Unix seconds.
type Token struct {
	Path   Path
	Born   int64
	Issued int64
}

// Status is what reading a token found.
type Status uint8

const (
	// Invalid is a token that is malformed, altered, keyed with another key or issued by
	// another ALB.
	Invalid Status = iota
	// Expired is a token this ALB issued that is past its ttl or idle timeout.
	Expired
	// Valid is a token this ALB issued that it still honors.
	Valid
)

// A token is a version, the path's depth, the two times, a member hash per level and a tag, all
// in base64url
const (
	tokenVersion = 1
	tagBytes     = secret.MinTagBytes
	offsetBorn   = 2
	offsetIssued = offsetBorn + 4
	offsetPath   = offsetIssued + 4
	minRawBytes  = offsetPath + 8 + tagBytes
	maxRawBytes  = offsetPath + 8*lb.MaxPickDepth + tagBytes
	maxTokenLen  = (maxRawBytes*8 + 5) / 6
)

// encoding is strict, so a token's unused trailing bits cannot vary
var encoding = base64.RawURLEncoding.Strict()

// Codec makes and reads one ALB's tokens. It is safe for concurrent use.
type Codec struct {
	signer    *secret.Signer
	bound     []byte
	ttl, idle int64
	bufs      sync.Pool
}

type buffers struct {
	text [maxTokenLen]byte
	raw  [maxRawBytes]byte
}

// NewCodec returns a codec for the named ALB's tokens, which it honors for ttl from when a
// session was first pinned and idle from when its token was issued; 0 is no limit.
func NewCodec(keys *secret.Keyring, albName string, ttl, idle time.Duration) (*Codec, error) {
	s, err := keys.Signer(Purpose, tagBytes)
	if err != nil {
		return nil, err
	}
	// the tag covers the ALB's name, length first, so that another ALB's token is not honored
	bound := binary.AppendUvarint(nil, uint64(len(albName)))
	c := &Codec{signer: s, bound: append(bound, albName...), ttl: seconds(ttl), idle: seconds(idle)}
	c.bufs.New = func() any { return new(buffers) }
	return c, nil
}

// seconds rounds a duration up to whole seconds, so a short one is not taken for no limit
func seconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

// Mint returns the token for t, or an empty string for a path with no levels.
func (c *Codec) Mint(t Token) string {
	depth := int(t.Path.Depth)
	if depth < 1 || depth > lb.MaxPickDepth {
		return ""
	}
	b := c.bufs.Get().(*buffers)
	raw := append(b.raw[:0], tokenVersion, t.Path.Depth)
	raw = binary.BigEndian.AppendUint32(raw, clampSeconds(t.Born))
	raw = binary.BigEndian.AppendUint32(raw, clampSeconds(t.Issued))
	for _, h := range t.Path.Hashes[:depth] {
		raw = binary.BigEndian.AppendUint64(raw, h)
	}
	raw = c.signer.Sum(raw, c.bound, raw)
	n := encoding.EncodedLen(len(raw))
	encoding.Encode(b.text[:n], raw)
	s := string(b.text[:n])
	c.bufs.Put(b)
	return s
}

// clampSeconds fits Unix seconds into a token's four bytes, which hold them until 2106
func clampSeconds(s int64) uint32 {
	if s <= 0 {
		return 0
	}
	if s >= math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(s)
}

// Read returns the token that s holds, when this ALB issued it, and whether it is honored at now,
// in Unix seconds.
func (c *Codec) Read(s string, now int64) (Token, Status) {
	if len(s) > maxTokenLen {
		return Token{}, Invalid
	}
	b := c.bufs.Get().(*buffers)
	n := copy(b.text[:], s)
	m, err := encoding.Decode(b.raw[:], b.text[:n])
	var t Token
	st := Invalid
	if err == nil {
		t, st = c.open(b.raw[:m], now)
	}
	c.bufs.Put(b)
	return t, st
}

func (c *Codec) open(raw []byte, now int64) (Token, Status) {
	if len(raw) < minRawBytes || raw[0] != tokenVersion {
		return Token{}, Invalid
	}
	depth := int(raw[1])
	if depth > lb.MaxPickDepth || len(raw) != offsetPath+8*depth+tagBytes {
		return Token{}, Invalid
	}
	body := raw[:len(raw)-tagBytes]
	if !c.signer.Verify(raw[len(body):], c.bound, body) {
		return Token{}, Invalid
	}
	t := Token{
		Path:   Path{Depth: uint8(depth)},
		Born:   int64(binary.BigEndian.Uint32(raw[offsetBorn:])),
		Issued: int64(binary.BigEndian.Uint32(raw[offsetIssued:])),
	}
	for i := range depth {
		t.Path.Hashes[i] = binary.BigEndian.Uint64(raw[offsetPath+8*i:])
	}
	if exp := c.Expires(t); exp != 0 && now >= exp {
		return t, Expired
	}
	return t, Valid
}

// Expires returns when the token stops being honored, in Unix seconds (0 is never): ttl after the
// session's first pin or idle after issue, whichever is first.
func (c *Codec) Expires(t Token) int64 {
	var exp int64
	if c.ttl > 0 {
		exp = t.Born + c.ttl
	}
	if c.idle > 0 && (exp == 0 || t.Issued+c.idle < exp) {
		exp = t.Issued + c.idle
	}
	return exp
}

// RefreshDue reports whether an honored token should be issued again at now, in Unix seconds:
// more than half of its idle timeout has passed, and its ttl leaves it more time to live.
func (c *Codec) RefreshDue(t Token, now int64) bool {
	return c.idle > 0 && 2*(now-t.Issued) > c.idle && (c.ttl == 0 || t.Born+c.ttl > t.Issued+c.idle)
}
