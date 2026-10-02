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

package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
)

// albStickyDoc is the projection of a generated ALB's session persistence
type albStickyDoc struct {
	Mode       string              `yaml:"mode,omitempty"`
	TTL        string              `yaml:"ttl,omitempty"`
	Idle       string              `yaml:"idle,omitempty"`
	Secret     string              `yaml:"secret,omitempty"`
	SecretFile string              `yaml:"secret_file,omitempty"`
	Cookie     *albStickyCookieDoc `yaml:"cookie,omitempty"`
	Header     *albStickyHeaderDoc `yaml:"header,omitempty"`
	Table      *albStickyTableDoc  `yaml:"table,omitempty"`
}

type albStickyCookieDoc struct {
	Name     string `yaml:"name,omitempty"`
	Secure   string `yaml:"secure,omitempty"`
	Lifetime string `yaml:"lifetime,omitempty"`
}

type albStickyHeaderDoc struct {
	Name string `yaml:"name,omitempty"`
}

type albStickyTableDoc struct {
	Key string `yaml:"key,omitempty"`
}

// generatedCookiePrefix begins the cookie name of a generated ALB whose session names none
const generatedCookiePrefix = so.DefaultCookieName + "_"

// generatedCookieHashBytes is how much of the ALB name's hash names its cookie: enough that two
// ALBs on one listener never share a name by chance
const generatedCookieHashBytes = 6

// CookieName is the cookie a generated ALB sets when its session names none: one of its own, since
// ALBs that share a listener and a host must not share a cookie
func CookieName(albName string) string {
	sum := sha256.Sum256([]byte(albName))
	return generatedCookiePrefix + hex.EncodeToString(sum[:generatedCookieHashBytes])
}

// noLimit is the ttl that gives a session no absolute limit, where an unset one takes the default
const noLimit = "0s"

// sessionSticky returns the sticky block that keeps a Gateway API session on the named ALB
func (e effective) sessionSticky(s *ir.Session, albName string) *albStickyDoc {
	d := &albStickyDoc{TTL: noLimit}
	e.keyTokens(d)
	if s.AbsoluteMS > 0 {
		d.TTL = (time.Duration(s.AbsoluteMS) * time.Millisecond).String()
	}
	if s.Type == ir.SessionHeader {
		d.Mode = so.ModeHeader
		if s.Name != "" {
			d.Header = &albStickyHeaderDoc{Name: s.Name}
		}
		return d
	}
	d.Mode = so.ModeCookie
	d.Cookie = &albStickyCookieDoc{Name: s.Name}
	if d.Cookie.Name == "" {
		d.Cookie.Name = CookieName(albName)
	}
	if !s.Permanent {
		d.Cookie.Lifetime = so.LifetimeSession
	}
	if so.SecureCookieName(d.Cookie.Name) {
		d.Cookie.Secure = so.SecureAlways
	}
	return d
}

// policySticky returns the sticky block a policy asks of the named ALB, or nil; a stream listener
// keeps sessions only in a table, and a key the listener cannot read is left at the default
func (e effective) policySticky(albName string, stream bool, readable func(flowkey.KeySource) bool,
) *albStickyDoc {
	if e.sticky == "" || e.sticky == reserved.ReferenceNone {
		return nil
	}
	d := &albStickyDoc{Mode: e.sticky}
	if stream {
		d.Mode = so.ModeTable
	}
	if e.stickyTTL > 0 {
		d.TTL = e.stickyTTL.String()
	}
	if e.stickyIdle > 0 {
		d.Idle = e.stickyIdle.String()
	}
	switch d.Mode {
	case so.ModeTable:
		if ks, err := flowkey.ParseKeySource(e.stickyKey); e.stickyKey != "" && err == nil && readable(ks) {
			d.Table = &albStickyTableDoc{Key: e.stickyKey}
		}
		return d
	case so.ModeCookie:
		d.Cookie = &albStickyCookieDoc{Name: CookieName(albName)}
	}
	e.keyTokens(d)
	return d
}

// keyTokens gives a block that issues tokens its key: the one a class's Secret holds, else the
// configured file, else neither, which keys them per process
func (e effective) keyTokens(d *albStickyDoc) {
	if e.stickySecret != "" {
		d.Secret = e.stickySecret
		return
	}
	d.SecretFile = e.stickySecretFile
}

// memberSticky returns the sticky block of the ALB balancing one member's endpoints: the session
// its Service asks for, else its policy's; none when the rule keeps the session for every member
func (e effective) memberSticky(rule *ir.Session, m ir.BackendMember, albName string) *albStickyDoc {
	switch {
	case rule != nil:
		return nil
	case m.Session != nil:
		return e.sessionSticky(m.Session, albName)
	}
	return e.policySticky(albName, false, flowkey.KeySource.OnHTTP)
}
