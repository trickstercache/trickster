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
package pgwire

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/loaders"
	pgo "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"
)

const (
	schemePostgres   = "postgres"
	schemePostgreSQL = "postgresql"
	// alpnPostgreSQL is the ALPN protocol libpq 17+ offers; a server config
	// advertising other protocols would fail those handshakes.
	alpnPostgreSQL = "postgresql"
)

// Upstream describes how sessions reach the origin.
type Upstream struct {
	// Address is the origin's TCP host:port.
	Address string
	// Host is the origin's host name, used for certificate verification.
	Host string
	// User and Password are the origin credentials from the origin_url.
	User     string
	Password string
	// Database is the default database from the origin_url path.
	Database string
	// TLS is nil when the upstream TLS mode is disable.
	TLS *tls.Config
}

// Config contains the protocol settings derived from one backend and its listener.
type Config struct {
	BackendName string
	// Provider is the canonical provider name, used as the metrics label.
	Provider   string
	RestartKey string
	Upstream   Upstream
	// Users holds the credentials Trickster authenticates clients against.
	// When nil, the client's authentication exchange is relayed to the origin.
	Users map[string]string
	// InboundTLS enables answering SSLRequest with a TLS handshake.
	InboundTLS *tls.Config
	// RequireSecureTransport rejects clients that do not upgrade to TLS.
	RequireSecureTransport   bool
	ConnectTimeout           time.Duration
	MaxUpstreamConnections   int64
	HandshakeTimeout         time.Duration
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration
	IdleTimeout              time.Duration
	MaxMessageSizeBytes      int
	AllowCleartextWithoutTLS bool
	AllowMD5                 bool
}

// Terminated reports whether Trickster authenticates clients itself and logs
// in to the origin with the backend's own credentials.
func (c *Config) Terminated() bool { return c.Users != nil }

// ConfigFromOptions derives the protocol configuration from backend options. The
// origin URL format is postgres://[user[:password]@]host[:port][/database].
func ConfigFromOptions(o *bo.Options, engine Engine) (Config, error) {
	if o == nil {
		return Config{}, errors.New("nil postgres backend options")
	}
	if engine == nil {
		return Config{}, fmt.Errorf("no postgres wire-protocol engine for provider %q", o.Provider)
	}
	if o.TLS != nil && (o.TLS.FullChainCertPath == "") != (o.TLS.PrivateKeyPath == "") {
		return Config{}, errors.New("postgres downstream TLS requires both full_chain_cert_path and private_key_path")
	}
	if o.RequireTLS && (o.TLS == nil || o.TLS.FullChainCertPath == "") {
		return Config{}, errors.New("postgres require_tls requires a downstream server certificate and private key")
	}
	upstream, err := upstreamFromOptions(o, engine)
	if err != nil {
		return Config{}, err
	}
	users, err := downstreamUsers(o)
	if err != nil {
		return Config{}, err
	}
	if users != nil && upstream.User == "" {
		return Config{}, errors.New("postgres origin URL must include a username when the backend has an authenticator")
	}
	c := Config{
		BackendName: o.Name, Provider: engine.Name(), Upstream: upstream, Users: users,
		RequireSecureTransport: o.RequireTLS,
		ConnectTimeout:         time.Duration(o.Timeout),
		MaxUpstreamConnections: int64(o.MaxConcurrentConns),
	}
	c.RestartKey = restartKey(o, users)
	c.ApplyListenerOptions(nil)
	return c, nil
}

// ApplyListenerOptions overlays limits owned by the downstream listener.
func (c *Config) ApplyListenerOptions(o *pgo.ListenerOptions) {
	if o == nil {
		o = pgo.NewListener()
	} else {
		c.RestartKey = checksum.Checksum(c.RestartKey + fmt.Sprintf("|%v", *o))
	}
	c.HandshakeTimeout = time.Duration(o.HandshakeTimeout)
	c.ReadTimeout = time.Duration(o.ReadTimeout)
	c.WriteTimeout = time.Duration(o.WriteTimeout)
	c.IdleTimeout = time.Duration(o.IdleTimeout)
	c.MaxMessageSizeBytes = o.MaxMessageSizeBytes
	c.AllowCleartextWithoutTLS = o.AllowCleartextWithoutTLS
	c.AllowMD5 = o.AllowMD5
}

func upstreamFromOptions(o *bo.Options, engine Engine) (Upstream, error) {
	u, err := url.Parse(o.OriginURL)
	if err != nil {
		return Upstream{}, fmt.Errorf("parse postgres origin URL: %w", err)
	}
	if u.Scheme != schemePostgres && u.Scheme != schemePostgreSQL {
		return Upstream{}, fmt.Errorf("unsupported postgres origin scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return Upstream{}, errors.New("postgres origin URL has no host")
	}
	port := u.Port()
	if port == "" {
		port = engine.DefaultPort()
	} else if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return Upstream{}, fmt.Errorf("invalid postgres origin port %q", port)
	}
	out := Upstream{Address: net.JoinHostPort(host, port), Host: host}
	if u.User != nil {
		out.User = u.User.Username()
		out.Password, _ = u.User.Password()
	}
	out.Database = strings.TrimPrefix(u.EscapedPath(), "/")
	if decoded, err := url.PathUnescape(out.Database); err == nil {
		out.Database = decoded
	}
	if o.TLS != nil && (o.TLS.ClientCertPath == "") != (o.TLS.ClientKeyPath == "") {
		return Upstream{}, errors.New("postgres upstream mutual TLS requires both client_cert_path and client_key_path")
	}
	out.TLS, err = upstreamTLS(o, host)
	return out, err
}

func upstreamTLS(o *bo.Options, host string) (*tls.Config, error) {
	mode := pgo.TLSModeDisable
	if o.Postgres != nil {
		mode = o.Postgres.UpstreamTLSMode
	}
	if mode == pgo.TLSModeDisable {
		return nil, nil
	}
	config, err := o.TLS.ToClientTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("postgres upstream TLS: %w", err)
	}
	if config == nil {
		config = &tls.Config{}
	}
	config.MinVersion = tls.VersionTLS12
	config.NextProtos = []string{alpnPostgreSQL}
	if config.ServerName == "" {
		config.ServerName = host
	}
	switch mode {
	case pgo.TLSModeRequire:
		config.InsecureSkipVerify = true // #nosec G402 -- operator-selected mode matching libpq sslmode=require
	case pgo.TLSModeVerifyCA:
		// Verify the chain but not the host name, as libpq sslmode=verify-ca does.
		// VerifyConnection also runs for resumed sessions; VerifyPeerCertificate does not.
		roots := config.RootCAs
		config.InsecureSkipVerify = true // #nosec G402 -- the chain is verified by VerifyConnection below
		config.VerifyConnection = func(state tls.ConnectionState) error {
			return verifyChain(state.PeerCertificates, roots)
		}
	}
	return config, nil
}

func verifyChain(certs []*x509.Certificate, roots *x509.CertPool) error {
	if len(certs) == 0 {
		return errors.New("postgres origin presented no certificate")
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	_, err := certs[0].Verify(opts)
	return err
}

func downstreamUsers(o *bo.Options) (map[string]string, error) {
	if o.AuthenticatorName == "" || o.AuthOptions == nil || o.AuthOptions.ObserveOnly {
		return nil, nil
	}
	users := maps.Clone(map[string]string(o.AuthOptions.Users))
	if users == nil {
		users = make(map[string]string)
	}
	if o.AuthOptions.UsersFile != "" {
		loaded, err := loaders.LoadData(o.AuthOptions.UsersFile, o.AuthOptions.UsersFileFormat)
		if err != nil {
			return nil, fmt.Errorf("load postgres authenticator users: %w", err)
		}
		maps.Copy(users, loaded)
	}
	if len(users) == 0 {
		return nil, errors.New("postgres authenticator has no users")
	}
	for user, credential := range users {
		if user == "" || credential == "" {
			return nil, errors.New("postgres authenticator usernames and passwords cannot be empty")
		}
		if cred.IsSCRAMVerifier(credential) {
			if _, err := cred.ParseSCRAMVerifier(credential); err != nil {
				return nil, fmt.Errorf("postgres authenticator user %q: %w", user, err)
			}
		}
	}
	return users, nil
}

func restartKey(o *bo.Options, users map[string]string) string {
	var identity strings.Builder
	for _, field := range []string{
		o.OriginURL, strconv.FormatInt(int64(o.Timeout), 10), strconv.Itoa(o.MaxConcurrentConns),
		strconv.FormatBool(o.RequireTLS), strconv.FormatBool(users != nil),
		tlsRestartIdentity(o), credentialRestartIdentity(users),
	} {
		appendRestartIdentityField(&identity, field)
	}
	if o.Postgres != nil {
		appendRestartIdentityField(&identity, fmt.Sprintf("%v", *o.Postgres))
	}
	return checksum.Checksum(identity.String())
}

func credentialRestartIdentity(users map[string]string) string {
	names := make([]string, 0, len(users))
	for name := range users {
		names = append(names, name)
	}
	slices.Sort(names)
	var identity strings.Builder
	for _, name := range names {
		appendRestartIdentityField(&identity, name)
		appendRestartIdentityField(&identity, users[name])
	}
	return identity.String()
}

func tlsRestartIdentity(o *bo.Options) string {
	// covers the configured paths and the files' contents, so
	// rotating a CA or client certificate in place restarts the listener.
	if o.TLS == nil {
		return ""
	}
	paths := append([]string{o.TLS.FullChainCertPath, o.TLS.PrivateKeyPath},
		o.TLS.CertificateAuthorityPaths...)
	paths = append(paths, o.TLS.ClientCertPath, o.TLS.ClientKeyPath)
	var identity strings.Builder
	appendRestartIdentityField(&identity, strconv.FormatBool(o.TLS.InsecureSkipVerify))
	appendRestartIdentityField(&identity, o.TLS.ServerName)
	for _, path := range paths {
		appendRestartIdentityField(&identity, path)
		if path == "" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			appendRestartIdentityField(&identity, checksum.Checksum(string(data)))
		}
	}
	return identity.String()
}

func appendRestartIdentityField(identity *strings.Builder, value string) {
	identity.WriteString(strconv.Itoa(len(value)))
	identity.WriteByte(':')
	identity.WriteString(value)
}
