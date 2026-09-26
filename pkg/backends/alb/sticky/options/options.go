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

// Package options configures an ALB's session persistence: how a client is kept on the member
// it was first sent to, and for how long.
package options

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/secret"
	"github.com/trickstercache/trickster/v2/pkg/util/keytable"

	"golang.org/x/net/http/httpguts"
)

// The ways a client's member is remembered
const (
	ModeCookie = "cookie"
	ModeHeader = "header"
	ModeTable  = "table"
)

// What happens to a request or flow whose pinned member is unavailable
const (
	OnUnavailableRepick = "repick"
	OnUnavailableReject = "reject"
)

// Where table mode reads a key to store a pin under
const (
	LearnRequest  = "request"
	LearnResponse = "response"
)

// When the cookie is marked Secure: auto marks it when the request arrived over TLS
const (
	SecureAuto   = "auto"
	SecureAlways = "true"
	SecureNever  = "false"
)

// A cookie's lifetime: permanent gives it a Max-Age that ends with its token, and session leaves
// it to end with the browser session while the ttl still ends its token
const (
	LifetimePermanent = "permanent"
	LifetimeSession   = "session"
)

// The cookie's SameSite values
const (
	SameSiteLax    = "lax"
	SameSiteStrict = "strict"
	SameSiteNone   = "none"
)

// The defaults
const (
	DefaultTTL        = time.Hour
	DefaultCookieName = "trickster_sticky"
	DefaultCookiePath = "/"
	DefaultHeaderName = "X-Trickster-Session"
	DefaultMaxEntries = keytable.DefaultMaxEntries
)

// The cookie name prefixes that a browser accepts only from a cookie with matching attributes
const (
	cookiePrefixSecure = "__Secure-"
	cookiePrefixHost   = "__Host-"
)

// minDuration is the shortest ttl or idle timeout, as a token keeps time in whole seconds
const minDuration = time.Second

var (
	// ErrInvalidMode is returned for a sticky.mode that is not cookie, header or table.
	ErrInvalidMode = errors.New("'sticky.mode' must be cookie, header or table")
	// ErrInvalidOnUnavailable is returned for a sticky.on_unavailable that is not repick or reject.
	ErrInvalidOnUnavailable = errors.New("'sticky.on_unavailable' must be repick or reject")
	// ErrInvalidDuration is returned for a sticky.ttl or sticky.idle that is negative or under 1s.
	ErrInvalidDuration = errors.New("'sticky.ttl' and 'sticky.idle' must be 0 or at least 1s")
	// ErrBlockForOtherMode is returned for a cookie, header or table block that the mode does not use.
	ErrBlockForOtherMode = errors.New("block is not used by the sticky mode")
	// ErrInvalidCookie is returned for a sticky.cookie name, path or domain that a cookie cannot carry.
	ErrInvalidCookie = errors.New("invalid 'sticky.cookie'")
	// ErrInvalidSecure is returned for a sticky.cookie.secure that is not auto, true or false.
	ErrInvalidSecure = errors.New("'sticky.cookie.secure' must be auto, true or false")
	// ErrInvalidLifetime is returned for a sticky.cookie.lifetime that is not permanent or session.
	ErrInvalidLifetime = errors.New("'sticky.cookie.lifetime' must be permanent or session")
	// ErrInvalidSameSite is returned for a sticky.cookie.same_site that is not lax, strict or none.
	ErrInvalidSameSite = errors.New("'sticky.cookie.same_site' must be lax, strict or none")
	// ErrSameSiteNoneInsecure is returned for same_site: none on a cookie not always marked Secure:
	// a browser refuses such a cookie from any response that lacks Secure.
	ErrSameSiteNoneInsecure = errors.New("'sticky.cookie.same_site: none' requires secure: true")
	// ErrInvalidHeaderName is returned for a sticky.header.name that is not a valid header name.
	ErrInvalidHeaderName = errors.New("invalid 'sticky.header.name'")
	// ErrInvalidTableKey is returned for a sticky.table.key that names the shape of a request,
	// which no client's session can follow.
	ErrInvalidTableKey = errors.New("'sticky.table.key' cannot be method, path or query")
	// ErrInvalidLearn is returned for a sticky.table.learn that is not request or response.
	ErrInvalidLearn = errors.New("'sticky.table.learn' must be request or response")
	// ErrLearnResponseKey is returned for learn: response with a key a response cannot set.
	ErrLearnResponseKey = errors.New("'sticky.table.learn: response' requires a header:<name> or cookie:<name> key")
	// ErrInvalidIPv6Prefix is returned for a sticky.table.ipv6_prefix outside 1-128.
	ErrInvalidIPv6Prefix = errors.New("'sticky.table.ipv6_prefix' must be between 1 and 128")
	// ErrInvalidMaxEntries is returned for a negative sticky.table.max_entries.
	ErrInvalidMaxEntries = errors.New("'sticky.table.max_entries' cannot be negative")
)

// Options configure an ALB's session persistence.
type Options struct {
	// Mode is how a client's member is remembered: cookie or header, a keyed token the client
	// sends back (http only), or table, a pin the ALB keeps by a key read from each request or
	// flow. The default is cookie on http listeners and table on the others.
	Mode string `yaml:"mode,omitempty"`
	// TTL is how long a session lasts from when it is first pinned, however it is used; 0 ends a
	// cookie's with the browser session and gives a table's pins no limit. The default is 1h.
	TTL *timeconv.Duration `yaml:"ttl,omitempty"`
	// Idle ends a session that goes unused for this long; 0, the default, never does. A token is
	// issued again, with a fresh idle deadline, once more than half of it has passed.
	Idle timeconv.Duration `yaml:"idle,omitempty"`
	// OnUnavailable is what happens to a request or flow whose pinned member is unavailable:
	// repick, the default, sends it to another member and pins it there; reject refuses it.
	OnUnavailable string `yaml:"on_unavailable,omitempty"`
	// Secret keys the cookie and header tokens: at least 32 bytes, the same on every replica.
	// When neither it nor SecretFile is set, a random key is used, and tokens die with the process.
	Secret secret.Secret `yaml:"secret,omitempty"`
	// SecretFile is a file that holds the key, in place of Secret; trailing line breaks are ignored.
	SecretFile string `yaml:"secret_file,omitempty"`
	// Cookie configures the token's cookie in cookie mode.
	Cookie CookieOptions `yaml:"cookie,omitempty"`
	// Header configures the token's header in header mode.
	Header HeaderOptions `yaml:"header,omitempty"`
	// Table configures table mode.
	Table TableOptions `yaml:"table,omitempty"`

	keys *secret.Keyring
}

// CookieOptions configure the cookie that carries a token.
type CookieOptions struct {
	// Name is the cookie's name; the default is trickster_sticky.
	Name string `yaml:"name,omitempty"`
	// Path is the cookie's Path; the default is /.
	Path string `yaml:"path,omitempty"`
	// Domain is the cookie's Domain; unset by default, which keeps the cookie to the host that set it.
	Domain string `yaml:"domain,omitempty"`
	// Secure is auto, the default, which marks the cookie Secure when the request arrived over
	// TLS, or true or false.
	Secure string `yaml:"secure,omitempty"`
	// HTTPOnly keeps the cookie from scripts; the default is true.
	HTTPOnly *bool `yaml:"http_only,omitempty"`
	// SameSite is the cookie's SameSite: lax, the default, strict or none, which requires secure: true.
	SameSite string `yaml:"same_site,omitempty"`
	// Lifetime is permanent, the default, which gives the cookie a Max-Age that ends with its token
	// when the token expires, or session, which never does, so it ends with the browser session.
	Lifetime string `yaml:"lifetime,omitempty"`
	// MarkPrivate adds Cache-Control: private to a response that sets the cookie, so that a
	// shared cache does not store it.
	MarkPrivate bool `yaml:"mark_private,omitempty"`
}

// HeaderOptions configure the header that carries a token.
type HeaderOptions struct {
	// Name is the response header a token is issued in, and the request header a client sends it
	// back in; the default is X-Trickster-Session.
	Name string `yaml:"name,omitempty"`
}

// TableOptions configure table mode.
type TableOptions struct {
	// Key is what a pin is kept by: client_ip (the default), host, header:<name>,
	// cookie:<name>, query:<name>, sni, proxy_tlv:<type> or user.
	Key string `yaml:"key,omitempty"`
	// Learn is request, the default, to keep a pin by the key a request carried, or response to
	// also keep one by the value a response sets for a header:<name> or cookie:<name> key.
	Learn string `yaml:"learn,omitempty"`
	// IPv6Prefix is how many leading bits of an IPv6 client address form a client_ip key.
	// The default, 64, keeps a client that rotates its privacy address on one member.
	IPv6Prefix int `yaml:"ipv6_prefix,omitempty"`
	// MaxEntries bounds the table; a new pin in a full table drops the least recently used of a
	// sample. The default is 100000.
	MaxEntries int `yaml:"max_entries,omitempty"`
	// KeySource is Key, parsed
	KeySource flowkey.KeySource `yaml:"-"`
}

// usesCookie reports whether the mode may issue cookies: cookie mode, or the http default
func (o *Options) usesCookie() bool {
	return o.Mode == "" || o.Mode == ModeCookie
}

// usesTable reports whether the mode may keep a table: table mode, or the default elsewhere
func (o *Options) usesTable() bool {
	return o.Mode == "" || o.Mode == ModeTable
}

// Clone returns a deep copy of the options.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	c := *o
	if o.TTL != nil {
		ttl := *o.TTL
		c.TTL = &ttl
	}
	if o.Cookie.HTTPOnly != nil {
		httpOnly := *o.Cookie.HTTPOnly
		c.Cookie.HTTPOnly = &httpOnly
	}
	return &c
}

// Initialize fills in the defaults of the blocks the mode uses, parses the table key and reads
// the key that tokens are made with.
func (o *Options) Initialize() error {
	if o.TTL == nil {
		ttl := timeconv.Duration(DefaultTTL)
		o.TTL = &ttl
	}
	if o.OnUnavailable == "" {
		o.OnUnavailable = OnUnavailableRepick
	}
	if o.usesCookie() {
		o.Cookie.initialize()
	}
	if o.Mode == ModeHeader {
		if o.Header.Name == "" {
			o.Header.Name = DefaultHeaderName
		}
		o.Header.Name = textproto.CanonicalMIMEHeaderKey(o.Header.Name)
	}
	if o.usesTable() {
		if err := o.Table.initialize(); err != nil {
			return err
		}
	}
	keys, err := secret.LoadKeyring(o.Secret, o.SecretFile)
	if err != nil {
		field := "sticky.secret"
		if o.Secret == "" {
			field = "sticky.secret_file"
		}
		return fmt.Errorf("%s: %w", field, err)
	}
	o.keys = keys
	return nil
}

func (o *CookieOptions) initialize() {
	if o.Name == "" {
		o.Name = DefaultCookieName
	}
	if o.Path == "" {
		o.Path = DefaultCookiePath
	}
	if o.Secure == "" {
		o.Secure = SecureAuto
	}
	if o.HTTPOnly == nil {
		httpOnly := true
		o.HTTPOnly = &httpOnly
	}
	if o.SameSite == "" {
		o.SameSite = SameSiteLax
	}
	if o.Lifetime == "" {
		o.Lifetime = LifetimePermanent
	}
}

func (o *TableOptions) initialize() error {
	ks, err := flowkey.ParseKeySource(o.Key)
	if err != nil {
		return fmt.Errorf("sticky.table.key: %w", err)
	}
	o.KeySource = ks
	if o.Learn == "" {
		o.Learn = LearnRequest
	}
	if o.IPv6Prefix == 0 {
		o.IPv6Prefix = flowkey.DefaultIPv6Prefix
	}
	if o.MaxEntries == 0 {
		o.MaxEntries = DefaultMaxEntries
	}
	return nil
}

// Validate checks the options that do not depend on the listeners an ALB serves.
func (o *Options) Validate() error {
	switch o.Mode {
	case "", ModeCookie, ModeHeader, ModeTable:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidMode, o.Mode)
	}
	switch o.OnUnavailable {
	case "", OnUnavailableRepick, OnUnavailableReject:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidOnUnavailable, o.OnUnavailable)
	}
	for _, d := range []time.Duration{o.TTLDuration(), time.Duration(o.Idle)} {
		if d < 0 || (d > 0 && d < minDuration) {
			return fmt.Errorf("%w: %s", ErrInvalidDuration, d)
		}
	}
	if !o.usesCookie() && o.Cookie != (CookieOptions{}) {
		return fmt.Errorf("'sticky.cookie' %w %q", ErrBlockForOtherMode, o.Mode)
	}
	if o.Mode != ModeHeader && o.Header != (HeaderOptions{}) {
		return fmt.Errorf("'sticky.header' %w %q", ErrBlockForOtherMode, o.Mode)
	}
	if !o.usesTable() && o.Table != (TableOptions{}) {
		return fmt.Errorf("'sticky.table' %w %q", ErrBlockForOtherMode, o.Mode)
	}
	if err := o.Cookie.validate(); err != nil {
		return err
	}
	if o.Header.Name != "" && !httpguts.ValidHeaderFieldName(o.Header.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidHeaderName, o.Header.Name)
	}
	return o.Table.validate()
}

func (o *CookieOptions) validate() error {
	switch o.Secure {
	case "", SecureAuto, SecureAlways, SecureNever:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSecure, o.Secure)
	}
	switch o.Lifetime {
	case "", LifetimePermanent, LifetimeSession:
	default:
		return fmt.Errorf("%w: %q", ErrInvalidLifetime, o.Lifetime)
	}
	switch o.SameSite {
	case "", SameSiteLax, SameSiteStrict:
	case SameSiteNone:
		// auto would leave Secure off a request that arrived without TLS, as one behind a proxy that
		// ends TLS does, and a browser drops that cookie
		if o.Secure != SecureAlways {
			return ErrSameSiteNoneInsecure
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSameSite, o.SameSite)
	}
	if o.Name == "" && o.Path == "" && o.Domain == "" {
		return nil
	}
	name := o.Name
	if name == "" {
		name = DefaultCookieName
	}
	probe := &http.Cookie{
		Name: name, Value: "v", Path: o.Path, Domain: o.Domain,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
	if err := probe.Valid(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCookie, err)
	}
	if o.Path != "" && !strings.HasPrefix(o.Path, "/") {
		return fmt.Errorf("%w: path %q must begin with /", ErrInvalidCookie, o.Path)
	}
	// a browser drops a prefixed cookie that lacks the attributes its prefix promises
	if SecureCookieName(name) && o.Secure != SecureAlways {
		return fmt.Errorf("%w: a %s or %s name requires secure: true", ErrInvalidCookie,
			cookiePrefixSecure, cookiePrefixHost)
	}
	if strings.HasPrefix(name, cookiePrefixHost) && (o.Domain != "" || (o.Path != "" && o.Path != "/")) {
		return fmt.Errorf("%w: a %s name requires path / and no domain", ErrInvalidCookie, cookiePrefixHost)
	}
	return nil
}

func (o *TableOptions) validate() error {
	ks, err := flowkey.ParseKeySource(o.Key)
	if err != nil {
		return fmt.Errorf("sticky.table.key: %w", err)
	}
	if !ks.FollowsClient() {
		return fmt.Errorf("%w: %q", ErrInvalidTableKey, o.Key)
	}
	switch o.Learn {
	case "", LearnRequest:
	case LearnResponse:
		if !ks.OnHTTPResponse() {
			return fmt.Errorf("%w, not %q", ErrLearnResponseKey, o.Key)
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidLearn, o.Learn)
	}
	if o.IPv6Prefix < 0 || o.IPv6Prefix > 128 {
		return ErrInvalidIPv6Prefix
	}
	if o.MaxEntries < 0 {
		return ErrInvalidMaxEntries
	}
	return nil
}

// SecureCookieName reports whether a browser accepts a cookie of this name only when it is marked
// Secure, as a __Secure- or __Host- prefix promises.
func SecureCookieName(name string) bool {
	return strings.HasPrefix(name, cookiePrefixSecure) || strings.HasPrefix(name, cookiePrefixHost)
}

// TTLDuration returns how long a session lasts from when it is first pinned; 0 is no limit.
func (o *Options) TTLDuration() time.Duration {
	if o.TTL == nil {
		return DefaultTTL
	}
	return time.Duration(*o.TTL)
}

// Keys returns the keyring that tokens are made with, once Initialize has read it.
func (o *Options) Keys() *secret.Keyring {
	return o.keys
}

// ModeFor returns the mode in effect on http listeners or on the others: the configured mode,
// else cookie on http and table elsewhere.
func (o *Options) ModeFor(http bool) string {
	switch {
	case o.Mode != "":
		return o.Mode
	case http:
		return ModeCookie
	}
	return ModeTable
}

// KeyWarning returns, for an ALB that serves http listeners, the warning for tokens made with a
// random per-process key, which neither a restart nor another replica honors; empty when a key
// is configured or the mode in effect on http issues no token.
func (o *Options) KeyWarning(albName string) string {
	if o == nil || o.keys == nil || !o.keys.Ephemeral() {
		return ""
	}
	mode := o.ModeFor(true)
	if mode == ModeTable {
		return ""
	}
	return fmt.Sprintf("alb %q: sticky.secret is not set, so %s tokens are keyed with a random"+
		" per-process key, which neither a restart nor another replica honors."+
		" set sticky.secret or sticky.secret_file to keep sessions across them", albName, mode)
}

// SameTable reports whether a table kept under these options can carry on under p: the options
// that shape a table, its key, lifetimes and bound, are the same.
func (o *Options) SameTable(p *Options) bool {
	if o == nil || p == nil || !o.usesTable() || !p.usesTable() {
		return false
	}
	return o.Mode == p.Mode && o.TTLDuration() == p.TTLDuration() && o.Idle == p.Idle && o.Table == p.Table
}
