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

package mysql

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/loaders"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/collations"
	"vitess.io/vitess/go/mysql/replication"
	"vitess.io/vitess/go/mysql/sqlerror"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	vtparser "vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vtenv"
	"vitess.io/vitess/go/vt/vttls"
)

const protocolVersion = "8.0.0-trickster"

const resultBatchSize = 256

const warningCountQuery = "SHOW COUNT(*) WARNINGS"

var passwordHashPrefixes = [...]string{
	"$apr1$", "$1$", "$2a$", "$2b$", "$2y$", "$5$", "$6$",
}

// ProtocolConfig contains MySQL protocol settings derived from a backend.
// Socket admission limits remain listener settings; these fields describe the
// authenticated upstream session and can grow to include certificate policy
// without coupling it to the generic listener package.
type ProtocolConfig struct {
	BackendName string
	RestartKey  string
	Upstream    vtmysql.ConnParams
	// Downstream credentials come from the backend's named authenticator and
	// are deliberately separate from the upstream origin_url credentials.
	DownstreamUsers map[string]string
	InboundTLS      *tls.Config
	// RequireSecureTransport rejects downstream handshakes that do not use
	// MySQL's in-band TLS upgrade.
	RequireSecureTransport bool
	ConnectTimeout         time.Duration
	QueryTimeout           time.Duration
	MaxResultRows          int
	MaxResultSizeBytes     int64
	MaxUpstreamConnections int64
	HandshakeTimeout       time.Duration
	ReadTimeout            time.Duration
	WriteTimeout           time.Duration
	IdleTimeout            time.Duration
	MaxPacketSizeBytes     int
	MaxQuerySizeBytes      int
	Tracer                 *tracing.Tracer
	Cache                  cache.Cache
	CacheProvider          interface{ Cache() cache.Cache }
	CacheKeyPrefix         string
	CacheTTL               time.Duration
	MaxObjectSize          int64
	RetentionPoints        int
	BackfillWindow         time.Duration
	BackfillPoints         int
	ShardMaxRange          time.Duration
	ShardStep              time.Duration
	ShardMaxPoints         int
	DoesShard              bool
	ProxyOnly              bool
}

// ProtocolConfigFromOptions constructs a protocol config from backend options.
// The origin URL format is mysql://user:password@host[:port]/database.
func ProtocolConfigFromOptions(o *bo.Options) (ProtocolConfig, error) {
	if o == nil {
		return ProtocolConfig{}, errors.New("nil MySQL backend options")
	}
	if o.TLS != nil {
		if (o.TLS.FullChainCertPath == "") != (o.TLS.PrivateKeyPath == "") {
			return ProtocolConfig{}, errors.New("MySQL downstream TLS requires both full_chain_cert_path and private_key_path")
		}
	}
	if o.RequireTLS && (o.TLS == nil || o.TLS.FullChainCertPath == "") {
		return ProtocolConfig{}, errors.New("MySQL require_tls requires a downstream server certificate and private key")
	}
	params, err := upstreamConnParamsFromOptions(o)
	if err != nil {
		return ProtocolConfig{}, err
	}
	downstreamUsers, err := downstreamCredentials(o)
	if err != nil {
		return ProtocolConfig{}, err
	}
	mysqlOptions := o.MySQL
	if mysqlOptions == nil {
		mysqlOptions = mo.New()
	}
	config := ProtocolConfig{
		BackendName: o.Name, Upstream: params, DownstreamUsers: downstreamUsers,
		RequireSecureTransport: o.RequireTLS,
		ConnectTimeout:         time.Duration(o.Timeout), QueryTimeout: time.Duration(o.Timeout),
		MaxResultRows:          mysqlOptions.MaxResultRows,
		MaxResultSizeBytes:     int64(mysqlOptions.MaxResultSizeBytes),
		MaxUpstreamConnections: int64(o.MaxConcurrentConns), CacheKeyPrefix: o.CacheKeyPrefix,
		CacheTTL: time.Duration(o.TimeseriesTTL), MaxObjectSize: int64(o.MaxObjectSizeBytes),
		RetentionPoints: o.TimeseriesRetentionFactor,
		BackfillWindow:  time.Duration(o.BackfillTolerance),
		BackfillPoints:  o.BackfillTolerancePoints,
		ShardMaxRange:   time.Duration(o.MaxShardSizeTime),
		ShardStep:       time.Duration(o.ShardStep),
		ShardMaxPoints:  o.MaxShardSizePoints,
		DoesShard:       o.DoesShard,
		ProxyOnly:       o.ProxyOnly,
	}
	config.RestartKey = protocolRestartKey(o, downstreamUsers)
	return config, nil
}

func upstreamConnParamsFromOptions(o *bo.Options) (vtmysql.ConnParams, error) {
	if o == nil {
		return vtmysql.ConnParams{}, errors.New("nil MySQL backend options")
	}
	u, err := url.Parse(o.OriginURL)
	if err != nil {
		return vtmysql.ConnParams{}, fmt.Errorf("parse MySQL origin URL: %w", err)
	}
	if u.Scheme != providers.MySQL {
		return vtmysql.ConnParams{}, fmt.Errorf("unsupported MySQL origin scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return vtmysql.ConnParams{}, errors.New("MySQL origin URL has no host")
	}
	port := 3306
	if value := u.Port(); value != "" {
		port, err = strconv.Atoi(value)
		if err != nil {
			return vtmysql.ConnParams{}, fmt.Errorf("invalid MySQL origin port: %w", err)
		}
	}
	username := ""
	password := ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	if username == "" {
		return vtmysql.ConnParams{}, errors.New("MySQL origin URL must include a username")
	}
	if o.TLS != nil {
		if (o.TLS.ClientCertPath == "") != (o.TLS.ClientKeyPath == "") {
			return vtmysql.ConnParams{}, errors.New("MySQL upstream mutual TLS requires both client_cert_path and client_key_path")
		}
		if len(o.TLS.CertificateAuthorityPaths) > 1 {
			return vtmysql.ConnParams{}, errors.New("MySQL upstream TLS currently accepts one certificate_authority_path")
		}
	}
	params := vtmysql.ConnParams{
		Host: host, Port: port, Uname: username, Pass: password,
		DbName:           strings.TrimPrefix(u.EscapedPath(), "/"),
		ConnectTimeoutMs: uint64(max(time.Duration(o.Timeout).Milliseconds(), 1)),
		SslMode:          vttls.Disabled,
	}
	if decoded, decodeErr := url.PathUnescape(params.DbName); decodeErr == nil {
		params.DbName = decoded
	}
	configureUpstreamTLS(&params, o)
	return params, nil
}

func protocolRestartKey(o *bo.Options, users map[string]string) string {
	tlsIdentity := tlsRestartIdentity(o)
	credentials := credentialRestartIdentity(users)
	mysqlIdentity := ""
	if o.MySQL != nil {
		mysqlIdentity = fmt.Sprintf("%v", *o.MySQL)
	}
	value := fmt.Sprintf("%s|%d|%d|%s|%s|%d|%d|%d|%d|%d|%d|%d|%d|%t|%t|%t|%s|%s|%v", o.OriginURL,
		o.Timeout,
		o.MaxConcurrentConns,
		o.CacheName, o.CacheKeyPrefix, o.TimeseriesTTL, o.MaxObjectSizeBytes,
		o.TimeseriesRetentionFactor, o.BackfillTolerance, o.BackfillTolerancePoints,
		o.MaxShardSizeTime, o.ShardStep, o.MaxShardSizePoints, o.DoesShard,
		o.ProxyOnly, o.RequireTLS, tlsIdentity, credentials, mysqlIdentity)
	return checksum.Checksum(value)
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
	if o == nil || o.TLS == nil {
		return ""
	}
	identityPaths := append([]string{o.TLS.FullChainCertPath, o.TLS.PrivateKeyPath},
		o.TLS.CertificateAuthorityPaths...)
	identityPaths = append(identityPaths, o.TLS.ClientCertPath, o.TLS.ClientKeyPath)
	return fmt.Sprintf("%s|%s|%t|%s|%s|%s|%s", o.TLS.FullChainCertPath,
		o.TLS.PrivateKeyPath, o.TLS.InsecureSkipVerify,
		strings.Join(o.TLS.CertificateAuthorityPaths, "\x00"),
		o.TLS.ClientCertPath, o.TLS.ClientKeyPath, tlsFileIdentity(identityPaths...))
}

func appendRestartIdentityField(identity *strings.Builder, value string) {
	identity.WriteString(strconv.Itoa(len(value)))
	identity.WriteByte(':')
	identity.WriteString(value)
}

func tlsFileIdentity(paths ...string) string {
	var value strings.Builder
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			value.WriteString(path)
		} else {
			value.WriteString(checksum.Checksum(string(data)))
		}
		value.WriteByte(0)
	}
	return value.String()
}

// ApplyListenerOptions overlays limits owned by the downstream listener.
func (c *ProtocolConfig) ApplyListenerOptions(o *mo.ListenerOptions) {
	if o == nil {
		o = mo.NewListener()
	}
	c.HandshakeTimeout = time.Duration(o.HandshakeTimeout)
	c.ReadTimeout = time.Duration(o.ReadTimeout)
	c.WriteTimeout = time.Duration(o.WriteTimeout)
	c.IdleTimeout = time.Duration(o.IdleTimeout)
	c.MaxPacketSizeBytes = o.MaxPacketSizeBytes
	c.MaxQuerySizeBytes = o.MaxQuerySizeBytes
	c.RestartKey = checksum.Checksum(c.RestartKey + fmt.Sprintf("|%v", *o))
}

func downstreamCredentials(o *bo.Options) (map[string]string, error) {
	if o.AuthenticatorName == "" || o.AuthOptions == nil {
		return nil, errors.New("MySQL backend requires an authenticator_name")
	}
	if o.AuthOptions.ObserveOnly {
		return nil, errors.New("MySQL authenticator cannot be observe_only")
	}
	users := maps.Clone(map[string]string(o.AuthOptions.Users))
	if o.AuthOptions.UsersFile != "" {
		loaded, err := loaders.LoadData(o.AuthOptions.UsersFile, o.AuthOptions.UsersFileFormat)
		if err != nil {
			return nil, fmt.Errorf("load MySQL authenticator users: %w", err)
		}
		maps.Copy(users, loaded)
	}
	if len(users) == 0 {
		return nil, errors.New("MySQL authenticator has no users")
	}
	for user, password := range users {
		if user == "" || password == "" {
			return nil, errors.New("MySQL authenticator usernames and passwords cannot be empty")
		}
		if isPasswordHash(password) {
			return nil, fmt.Errorf("MySQL native authentication requires a plaintext credential for user %q", user)
		}
	}
	return users, nil
}

// DownstreamCredentialsFromOptions returns the plaintext native credentials
// owned by a listener-facing MySQL backend or User Router ALB.
func DownstreamCredentialsFromOptions(o *bo.Options) (map[string]string, error) {
	return downstreamCredentials(o)
}

func isPasswordHash(password string) bool {
	for _, prefix := range passwordHashPrefixes {
		if strings.HasPrefix(password, prefix) {
			return true
		}
	}
	return false
}

func configureUpstreamTLS(params *vtmysql.ConnParams, o *bo.Options) {
	if params == nil || o == nil || o.TLS == nil {
		return
	}
	tlsOptions := o.TLS
	if tlsOptions.InsecureSkipVerify {
		params.SslMode = vttls.Required
	} else if len(tlsOptions.CertificateAuthorityPaths) > 0 || tlsOptions.ClientCertPath != "" {
		params.SslMode = vttls.VerifyIdentity
	}
	if len(tlsOptions.CertificateAuthorityPaths) > 0 {
		params.SslCa = tlsOptions.CertificateAuthorityPaths[0]
	}
	params.SslCert = tlsOptions.ClientCertPath
	params.SslKey = tlsOptions.ClientKeyPath
}

// ProtocolServer terminates the downstream MySQL protocol and proxies commands
// through authenticated upstream sessions.
type ProtocolServer struct {
	config        ProtocolConfig
	handler       *protocolHandler
	routedHandler *routedProtocolHandler
	listener      *vtmysql.Listener
	mtx           sync.Mutex
}

// NewProtocolServer returns a server ready to serve an existing net.Listener.
func NewProtocolServer(config ProtocolConfig) (*ProtocolServer, error) {
	listenerDefaults := mo.NewListener()
	if config.HandshakeTimeout <= 0 {
		config.ApplyListenerOptions(listenerDefaults)
	}
	if config.MaxResultRows <= 0 {
		config.MaxResultRows = mo.DefaultMaxResultRows
	}
	if config.MaxResultSizeBytes <= 0 {
		config.MaxResultSizeBytes = mo.DefaultMaxResultSizeBytes
	}
	env, err := vtenv.New(vtenv.Options{MySQLServerVersion: protocolVersion})
	if err != nil {
		return nil, err
	}
	h := newProtocolHandler(config, env)
	return &ProtocolServer{config: config, handler: h}, nil
}

// NewRoutedProtocolServer returns a MySQL server whose authenticated
// connections are assigned to protocol-neutral User Router targets. A route is
// resolved once per connection and retained for the session lifetime.
func NewRoutedProtocolServer(config ProtocolConfig, resolver backends.RouteResolver,
	targetConfigs map[string]ProtocolConfig,
) (*ProtocolServer, error) {
	if resolver == nil {
		return nil, errors.New("MySQL routed protocol server requires a route resolver")
	}
	if len(targetConfigs) == 0 {
		return nil, errors.New("MySQL routed protocol server has no native targets")
	}
	if len(config.DownstreamUsers) == 0 {
		return nil, errors.New("MySQL routed protocol server has no listener-facing users")
	}
	listenerDefaults := mo.NewListener()
	if config.HandshakeTimeout <= 0 {
		config.ApplyListenerOptions(listenerDefaults)
	}
	env, err := vtenv.New(vtenv.Options{MySQLServerVersion: protocolVersion})
	if err != nil {
		return nil, err
	}
	routed := &routedProtocolHandler{
		env: env, resolver: resolver, targets: make(map[string]*protocolHandler, len(targetConfigs)),
		controls: make(map[uint32]*phaseConn),
	}
	for name, targetConfig := range targetConfigs {
		if targetConfig.HandshakeTimeout <= 0 {
			targetConfig.HandshakeTimeout = config.HandshakeTimeout
			targetConfig.ReadTimeout = config.ReadTimeout
			targetConfig.WriteTimeout = config.WriteTimeout
			targetConfig.IdleTimeout = config.IdleTimeout
			targetConfig.MaxPacketSizeBytes = config.MaxPacketSizeBytes
			targetConfig.MaxQuerySizeBytes = config.MaxQuerySizeBytes
		}
		if targetConfig.MaxResultRows <= 0 {
			targetConfig.MaxResultRows = mo.DefaultMaxResultRows
		}
		if targetConfig.MaxResultSizeBytes <= 0 {
			targetConfig.MaxResultSizeBytes = mo.DefaultMaxResultSizeBytes
		}
		routed.targets[name] = newProtocolHandler(targetConfig, env)
	}
	return &ProtocolServer{config: config, routedHandler: routed}, nil
}

func newProtocolHandler(config ProtocolConfig, env *vtenv.Environment) *protocolHandler {
	return &protocolHandler{
		config: config, env: env, sessions: make(map[*vtmysql.Conn]*upstreamSession),
		controls: make(map[uint32]*phaseConn), dpcLocks: make(map[string]*dpcLock),
		metricHandles: newProtocolMetricHandles(config.BackendName),
	}
}

// Serve runs the protocol accept loop on l.
func (s *ProtocolServer) Serve(l net.Listener) error {
	if s.config.RequireSecureTransport && s.config.InboundTLS == nil {
		return errors.New("MySQL secure transport requires an inbound TLS configuration")
	}
	var handler vtmysql.Handler = s.handler
	var resolver backends.RouteResolver
	if s.routedHandler != nil {
		handler = s.routedHandler
		resolver = s.routedHandler
	}
	auth := newCredentialAuth(s.config.DownstreamUsers, s.config.BackendName, resolver)
	listener, err := vtmysql.NewFromListener(l, auth, handler, 0, 0,
		false, true, 0, 0, false)
	if err != nil {
		return err
	}
	if s.config.InboundTLS != nil {
		listener.TLSConfig.Store(s.config.InboundTLS)
	}
	listener.RequireSecureTransport = s.config.RequireSecureTransport
	listener.PreHandleFunc = func(_ context.Context, conn net.Conn, connectionID uint32) (net.Conn, error) {
		controlled := newPhaseConn(conn, s.config.HandshakeTimeout, s.config.ReadTimeout,
			s.config.WriteTimeout, s.config.IdleTimeout)
		if s.routedHandler != nil {
			s.routedHandler.setControl(connectionID, controlled)
		} else {
			s.handler.setControl(connectionID, controlled)
		}
		return controlled, nil
	}
	s.mtx.Lock()
	s.listener = listener
	s.mtx.Unlock()
	listener.Accept()
	return nil
}

// Shutdown stops admission, closes active upstream sessions, and waits for
// their downstream handlers to leave until ctx expires.
func (s *ProtocolServer) Shutdown(ctx context.Context) error {
	s.mtx.Lock()
	listener := s.listener
	s.mtx.Unlock()
	if listener != nil {
		listener.Shutdown()
	}
	if s.routedHandler != nil {
		return s.routedHandler.shutdown(ctx)
	}
	return s.handler.shutdown(ctx)
}

// UpdateTLSConfig atomically rotates the certificate used by new in-band TLS
// handshakes. Existing TLS sessions keep their negotiated certificate state.
func (s *ProtocolServer) UpdateTLSConfig(config *tls.Config) {
	s.mtx.Lock()
	s.config.InboundTLS = config
	listener := s.listener
	s.mtx.Unlock()
	if listener != nil && config != nil {
		listener.TLSConfig.Store(config)
	}
}

// UpdateRouteResolver atomically switches new routed connections to a resolver
// built from the reloaded backend graph. Existing sessions retain their target.
func (s *ProtocolServer) UpdateRouteResolver(resolver backends.RouteResolver) {
	if s.routedHandler != nil && resolver != nil {
		s.routedHandler.setResolver(resolver)
	}
}

// ProtocolRestartKey identifies the immutable transport/authentication state
// held by this running server, including the certificate file contents loaded
// when it was created.
func (s *ProtocolServer) ProtocolRestartKey() string { return s.config.RestartKey }

type credentialAuth struct {
	backend  string
	users    map[string][]byte
	methods  []vtmysql.AuthMethod
	resolver backends.RouteResolver
}

func newCredentialAuth(users map[string]string, backend string,
	resolver backends.RouteResolver,
) *credentialAuth {
	credentials := make(map[string][]byte, len(users))
	for username, password := range users {
		credentials[username] = []byte(password)
	}
	a := &credentialAuth{backend: backend, users: credentials, resolver: resolver}
	a.methods = []vtmysql.AuthMethod{vtmysql.NewMysqlNativeAuthMethod(a, a)}
	return a
}

func (a *credentialAuth) AuthMethods() []vtmysql.AuthMethod { return a.methods }

func (a *credentialAuth) DefaultAuthMethodDescription() vtmysql.AuthMethodDescription {
	return vtmysql.MysqlNativePassword
}

func (a *credentialAuth) HandleUser(user string) bool {
	_, ok := a.users[user]
	return ok
}

func (a *credentialAuth) UserEntryWithHash(c *vtmysql.Conn, salt []byte, user string,
	authResponse []byte, _ net.Addr,
) (vtmysql.Getter, error) {
	password, ok := a.users[user]
	expected := vtmysql.ScrambleMysqlNativePassword(salt, password)
	if !ok || subtle.ConstantTimeCompare(expected, authResponse) != 1 {
		metrics.MySQLConnectionErrors.WithLabelValues(a.backend, "authentication").Inc()
		return nil, sqlerror.NewSQLErrorf(sqlerror.ERAccessDeniedError,
			sqlerror.SSAccessDeniedError, "access denied for user %q", user)
	}
	if a.resolver != nil {
		decision, resolved := a.resolver.ResolveRoute(backends.RouteInput{
			RouterName: a.backend, Username: user, Authenticated: true,
		})
		if !resolved || !decision.Target.Available() {
			outcome := decision.Outcome
			if outcome == "" {
				outcome = backends.RouteOutcomeNoRoute
			}
			metrics.MySQLRouteSelections.WithLabelValues(a.backend, "", string(outcome)).Inc()
			metrics.MySQLConnectionErrors.WithLabelValues(a.backend, "route").Inc()
			return nil, sqlerror.NewSQLErrorf(sqlerror.ERAccessDeniedError,
				sqlerror.SSAccessDeniedError, "no available MySQL route for user %q", user)
		}
		if decision.Outcome == "" {
			decision.Outcome = backends.RouteOutcomeSelected
		}
		metrics.MySQLRouteSelections.WithLabelValues(a.backend,
			decision.Target.Backend.Name(), string(decision.Outcome)).Inc()
		c.ClientData = decision
	}
	return &vtmysql.StaticUserData{Username: user}, nil
}

type upstreamSession struct {
	mtx                 sync.Mutex
	conn                *vtmysql.Conn
	upstream            vtmysql.ConnParams
	upstreamParamsReady bool
	warnings            uint16
	database            string
	timeZone            string
	collation           collations.ID // effective upstream collation
	inTx                bool
	cacheUnsafe         bool
	// stateful marks session state that replaySessionState cannot reproduce on
	// a replacement connection: user variables, advisory locks, temporary
	// objects, table locks, and the diagnostics a write leaves behind. Only the
	// database and time zone are replayable. Every statement that sets this
	// currently also sets cacheUnsafe, but the two answer different questions —
	// cache correctness for this session's reads versus reconnect safety — and
	// are tracked apart so neither policy silently moves the other.
	stateful bool
	// terminal marks a session whose upstream was lost while it held state a
	// reconnect cannot reproduce. It refuses a later transparent reconnect.
	terminal        bool
	downstream      *vtmysql.Conn
	control         *phaseConn
	ready           bool
	forced          bool
	upstreamCounted bool
	traceContext    context.Context
	connectSpan     trace.Span
}

// routedConnection records the one-time outcome of route activation for a
// downstream connection. A non-nil routedConnection with no target marks the
// activation as permanently failed, so no later callback can select a second
// target for the same connection.
type routedConnection struct {
	target *protocolHandler
}

// routedProtocolHandler adapts Vitess's protocol-specific callbacks to a
// protocol-neutral route decision made during authentication. Each connection
// retains one target so transaction and prepared-statement state cannot cross
// backend boundaries.
type routedProtocolHandler struct {
	vtmysql.UnimplementedHandler
	env      *vtenv.Environment
	resolver backends.RouteResolver
	targets  map[string]*protocolHandler
	mtx      sync.Mutex
	controls map[uint32]*phaseConn
}

func (h *routedProtocolHandler) Env() *vtenv.Environment { return h.env }

func (h *routedProtocolHandler) ResolveRoute(input backends.RouteInput) (backends.RouteDecision, bool) {
	h.mtx.Lock()
	resolver := h.resolver
	h.mtx.Unlock()
	if resolver == nil {
		return backends.RouteDecision{}, false
	}
	return resolver.ResolveRoute(input)
}

func (h *routedProtocolHandler) setResolver(resolver backends.RouteResolver) {
	h.mtx.Lock()
	h.resolver = resolver
	h.mtx.Unlock()
}

func (h *routedProtocolHandler) setControl(connectionID uint32, control *phaseConn) {
	h.mtx.Lock()
	h.controls[connectionID] = control
	h.mtx.Unlock()
}

func (h *routedProtocolHandler) NewConnection(c *vtmysql.Conn) {
	c.StatusFlags = vtmysql.ServerStatusAutocommit
}

// activate consumes the route decision that authentication left on the
// connection and initializes the selected target exactly once. Vitess
// dispatches the initial USE of a CLIENT_CONNECT_WITH_DB handshake before it
// reports the connection as ready, so that command activates the route and
// ConnectionReady only completes the handshake on an already-activated target.
func (h *routedProtocolHandler) activate(c *vtmysql.Conn) (*protocolHandler, error) {
	if c == nil {
		return nil, errNoRoute()
	}
	if routed, ok := c.ClientData.(*routedConnection); ok {
		if routed.target == nil {
			return nil, errNoRoute()
		}
		return routed.target, nil
	}
	var target *protocolHandler
	if decision, ok := c.ClientData.(backends.RouteDecision); ok && decision.Target.Backend != nil {
		target = h.targets[decision.Target.Backend.Name()]
	}
	control := h.takeControl(c.ConnectionID)
	if target == nil {
		// Activation failure is terminal for the connection. Recording it
		// releases the pending control and blocks a second target selection.
		c.ClientData = &routedConnection{}
		c.MarkForClose()
		return nil, errNoRoute()
	}
	if control != nil {
		target.setControl(c.ConnectionID, control)
	}
	c.ClientData = &routedConnection{target: target}
	target.NewConnection(c)
	return target, nil
}

func (h *routedProtocolHandler) ConnectionReady(c *vtmysql.Conn) {
	target, err := h.activate(c)
	if err != nil {
		c.MarkForClose()
		return
	}
	target.ConnectionReady(c)
}

func (h *routedProtocolHandler) takeControl(connectionID uint32) *phaseConn {
	h.mtx.Lock()
	control := h.controls[connectionID]
	delete(h.controls, connectionID)
	h.mtx.Unlock()
	return control
}

func (h *routedProtocolHandler) target(c *vtmysql.Conn) (*protocolHandler, error) {
	if c != nil {
		if routed, ok := c.ClientData.(*routedConnection); ok && routed.target != nil {
			return routed.target, nil
		}
	}
	return nil, errNoRoute()
}

func errNoRoute() error {
	return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSUnknownSQLState,
		"Trickster has no MySQL route for this connection")
}

func (h *routedProtocolHandler) ConnectionClosed(c *vtmysql.Conn) {
	if target, err := h.target(c); err == nil {
		target.ConnectionClosed(c)
	}
	h.mtx.Lock()
	delete(h.controls, c.ConnectionID)
	h.mtx.Unlock()
}

func (h *routedProtocolHandler) ComResetConnection(c *vtmysql.Conn) {
	if target, err := h.target(c); err == nil {
		target.ComResetConnection(c)
	} else {
		c.MarkForClose()
	}
}

func (h *routedProtocolHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	// ComQuery is the only callback Vitess can dispatch before ConnectionReady,
	// so it activates the route rather than requiring one to already exist.
	target, err := h.activate(c)
	if err != nil {
		return err
	}
	return target.ComQuery(c, query, callback)
}

func (h *routedProtocolHandler) ComQueryMulti(c *vtmysql.Conn, query string,
	callback func(sqltypes.QueryResponse, bool, bool) error,
) error {
	target, err := h.target(c)
	if err != nil {
		return err
	}
	return target.ComQueryMulti(c, query, callback)
}

func (h *routedProtocolHandler) ComPrepare(c *vtmysql.Conn, query string) ([]*querypb.Field, uint16, error) {
	target, err := h.target(c)
	if err != nil {
		return nil, 0, err
	}
	return target.ComPrepare(c, query)
}

func (h *routedProtocolHandler) ComStmtExecute(c *vtmysql.Conn, prepare *vtmysql.PrepareData,
	callback func(*sqltypes.Result) error,
) error {
	target, err := h.target(c)
	if err != nil {
		return err
	}
	return target.ComStmtExecute(c, prepare, callback)
}

func (h *routedProtocolHandler) ComRegisterReplica(c *vtmysql.Conn, host string,
	port uint16, user, password string,
) error {
	target, err := h.target(c)
	if err != nil {
		return err
	}
	return target.ComRegisterReplica(c, host, port, user, password)
}

func (h *routedProtocolHandler) ComBinlogDump(c *vtmysql.Conn, file string, position uint32) error {
	target, err := h.target(c)
	if err != nil {
		return err
	}
	return target.ComBinlogDump(c, file, position)
}

func (h *routedProtocolHandler) ComBinlogDumpGTID(c *vtmysql.Conn, file string,
	position uint64, set replication.GTIDSet, flags uint16,
) error {
	target, err := h.target(c)
	if err != nil {
		return err
	}
	return target.ComBinlogDumpGTID(c, file, position, set, flags)
}

func (h *routedProtocolHandler) WarningCount(c *vtmysql.Conn) uint16 {
	if target, err := h.target(c); err == nil {
		return target.WarningCount(c)
	}
	return 0
}

func (h *routedProtocolHandler) shutdown(ctx context.Context) error {
	var firstErr error
	for _, target := range h.targets {
		if err := target.shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type protocolHandler struct {
	vtmysql.UnimplementedHandler
	config          ProtocolConfig
	env             *vtenv.Environment
	mtx             sync.Mutex
	sessions        map[*vtmysql.Conn]*upstreamSession
	controls        map[uint32]*phaseConn
	wg              sync.WaitGroup
	closed          atomic.Bool
	activeUpstreams atomic.Int64
	opcGroup        singleflight.Group
	dpcLockMtx      sync.Mutex
	dpcLocks        map[string]*dpcLock
	metricHandles   *protocolMetricHandles
}

type dpcLock struct {
	sync.Mutex
	references int
}

func (h *protocolHandler) Env() *vtenv.Environment { return h.env }

func (h *protocolHandler) setControl(connectionID uint32, control *phaseConn) {
	h.mtx.Lock()
	h.controls[connectionID] = control
	h.mtx.Unlock()
}

func (h *protocolHandler) NewConnection(c *vtmysql.Conn) {
	c.StatusFlags = vtmysql.ServerStatusAutocommit
	metrics.MySQLConnections.WithLabelValues(h.config.BackendName, "accepted").Inc()
	ctx := context.Background()
	var connectionSpan trace.Span
	if h.config.Tracer != nil && h.config.Tracer.Tracer != nil {
		ctx, connectionSpan = h.config.Tracer.Start(ctx, "mysql.connection",
			trace.WithAttributes(attribute.String("trickster.backend", h.config.BackendName)))
	}
	h.mtx.Lock()
	if !h.closed.Load() {
		h.sessions[c] = &upstreamSession{
			database: h.config.Upstream.DbName, upstream: h.config.Upstream, upstreamParamsReady: true,
			downstream: c, control: h.controls[c.ConnectionID], traceContext: ctx,
			connectSpan: connectionSpan,
		}
		h.wg.Add(1)
		metrics.MySQLActiveConnections.WithLabelValues(h.config.BackendName).Inc()
	} else {
		c.MarkForClose()
		if connectionSpan != nil {
			connectionSpan.End()
		}
	}
	h.mtx.Unlock()
}

func (h *protocolHandler) ConnectionReady(c *vtmysql.Conn) {
	h.mtx.Lock()
	session := h.sessions[c]
	h.mtx.Unlock()
	if session == nil {
		c.MarkForClose()
		return
	}
	if session.control != nil {
		session.control.setReady()
	}
	session.mtx.Lock()
	h.applyDownstreamHandshakeLocked(session, c)
	session.ready = true
	connectionSpan := session.connectSpan
	session.connectSpan = nil
	session.mtx.Unlock()
	if connectionSpan != nil {
		connectionSpan.End()
	}
	metrics.MySQLConnections.WithLabelValues(h.config.BackendName, "authenticated").Inc()
}

func (h *protocolHandler) ConnectionClosed(c *vtmysql.Conn) {
	h.closeSession(c, true)
}

func (h *protocolHandler) ComResetConnection(c *vtmysql.Conn) {
	h.closeSession(c, false)
	c.StatusFlags = vtmysql.ServerStatusAutocommit
	h.mtx.Lock()
	if !h.closed.Load() {
		session := &upstreamSession{
			database: h.config.Upstream.DbName, upstream: h.config.Upstream, upstreamParamsReady: true,
			downstream: c, ready: true, traceContext: context.Background(),
		}
		// The reset session restarts from the connection's original handshake
		// state, not from whatever collation the closed session last used.
		h.applyDownstreamHandshakeLocked(session, c)
		h.sessions[c] = session
	}
	h.mtx.Unlock()
}

func (h *protocolHandler) sessionState(c *vtmysql.Conn) (*upstreamSession, error) {
	h.mtx.Lock()
	session := h.sessions[c]
	h.mtx.Unlock()
	if h.closed.Load() || session == nil {
		return nil, sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSUnknownSQLState,
			"Trickster MySQL listener is shutting down")
	}
	return session, nil
}

func (h *protocolHandler) connectSession(session *upstreamSession) error {
	started := time.Now()
	defer func() {
		if h.metricHandles != nil {
			h.metricHandles.connectLatency.Observe(time.Since(started).Seconds())
			return
		}
		metrics.MySQLCommandLatency.WithLabelValues(h.config.BackendName,
			"connect").Observe(time.Since(started).Seconds())
	}()
	if session == nil {
		return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSUnknownSQLState,
			"Trickster MySQL listener has no session")
	}
	session.mtx.Lock()
	defer session.mtx.Unlock()
	if session.terminal {
		// Reconnecting here would silently move the client's remaining
		// statements onto a fresh autocommit connection that has none of the
		// transaction, temporary-object, lock, or variable state it expects.
		return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSUnknownSQLState,
			"Trickster lost MySQL origin session state that cannot be restored")
	}
	if !session.upstreamParamsReady {
		session.upstream = h.config.Upstream
		session.upstreamParamsReady = true
	}
	h.applyDownstreamHandshakeLocked(session, session.downstream)
	if session.conn != nil {
		return nil
	}
	params := session.upstream
	timeZone := session.timeZone
	if !h.reserveUpstream() {
		metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "upstream_admission").Inc()
		return sqlerror.NewSQLError(sqlerror.ERTooManyUserConnections, sqlerror.SSClientError,
			"Trickster MySQL origin connection limit reached")
	}
	reserved := true
	defer func() {
		if reserved {
			h.activeUpstreams.Add(-1)
		}
	}()
	ctx := context.Background()
	var span trace.Span
	if h.config.Tracer != nil && h.config.Tracer.Tracer != nil {
		ctx, span = h.config.Tracer.Start(session.traceContext, "mysql.origin.connect",
			trace.WithAttributes(attribute.String("trickster.backend", h.config.BackendName)))
		defer span.End()
	}
	if h.config.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.config.ConnectTimeout)
		defer cancel()
	}
	conn, err := vtmysql.Connect(ctx, &params)
	if err != nil {
		metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "upstream_connect").Inc()
		return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSNetError,
			"Trickster could not connect to the MySQL origin")
	}
	if err = h.replaySessionState(conn, timeZone); err != nil {
		conn.Close()
		metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "session_replay").Inc()
		return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSNetError,
			"Trickster could not restore the MySQL origin session")
	}
	if h.closed.Load() {
		conn.Close()
		return sqlerror.NewSQLError(sqlerror.CRServerGone, sqlerror.SSUnknownSQLState,
			"Trickster MySQL listener is shutting down")
	}
	session.conn = conn
	session.upstreamCounted = true
	reserved = false
	return nil
}

// applyDownstreamHandshakeLocked copies the state Vitess negotiated with the
// downstream client onto the session's private upstream parameters. Both the
// capability flags and the collation ID are fixed for the connection's
// lifetime, so a later upstream reconnect reproduces the same origin session.
func (h *protocolHandler) applyDownstreamHandshakeLocked(session *upstreamSession,
	downstream *vtmysql.Conn,
) {
	if session == nil || downstream == nil {
		return
	}
	session.upstream.Flags &^= uint64(vtmysql.CapabilityClientFoundRows)
	session.upstream.Flags |= uint64(downstream.Capabilities & vtmysql.CapabilityClientFoundRows)
	// A client that sends collation 0 asks for the server default, which leaves
	// the configured upstream collation in place.
	if downstream.CharacterSet != collations.Unknown {
		session.upstream.Charset = downstream.CharacterSet
	}
	// The cache identity tracks the collation the origin will actually use, so
	// a client that defers to the configured default is not split into its own
	// key space.
	session.collation = session.upstream.Charset
}

func (h *protocolHandler) replaySessionState(conn *vtmysql.Conn, timeZone string) error {
	if conn == nil || timeZone == "" {
		return nil
	}
	raw := conn.GetRawConn()
	timeout := h.config.QueryTimeout
	if timeout <= 0 {
		timeout = h.config.ConnectTimeout
	}
	if raw != nil && timeout > 0 {
		_ = raw.SetDeadline(time.Now().Add(timeout))
		defer func() { _ = raw.SetDeadline(time.Time{}) }()
	}
	_, err := conn.ExecuteFetch("SET time_zone = "+sqltypes.EncodeStringSQL(timeZone), 1, false)
	return err
}

func (h *protocolHandler) reserveUpstream() bool {
	limit := h.config.MaxUpstreamConnections
	if limit <= 0 {
		h.activeUpstreams.Add(1)
		return true
	}
	for {
		current := h.activeUpstreams.Load()
		if current >= limit {
			return false
		}
		if h.activeUpstreams.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (h *protocolHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	started := time.Now()
	defer func() {
		if h.metricHandles != nil {
			h.metricHandles.queryLatency.Observe(time.Since(started).Seconds())
			return
		}
		metrics.MySQLCommandLatency.WithLabelValues(h.config.BackendName,
			metricPathQuery).Observe(time.Since(started).Seconds())
	}()
	if h.config.MaxQuerySizeBytes > 0 && len(query) > h.config.MaxQuerySizeBytes {
		metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "query_size").Inc()
		c.MarkForClose()
		return sqlerror.NewSQLError(sqlerror.ERNetPacketTooLarge, sqlerror.SSNetError,
			"MySQL query exceeds Trickster's configured limit")
	}
	noBackslashEscapes := c.StatusFlags&vtmysql.ServerStatusNoBackslashEscapes != 0
	if feature := unsupportedTextFeature(query, noBackslashEscapes); feature != "" {
		return h.unsupported(feature)
	}
	if multiple, err := hasMultipleStatements(query); err != nil {
		metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "protocol").Inc()
		return sqlerror.NewSQLErrorf(sqlerror.ERParseError, sqlerror.SSClientError,
			"cannot parse MySQL statement boundaries: %v", err)
	} else if multiple {
		return h.unsupported("multi-statements")
	}
	session, err := h.sessionState(c)
	if err != nil {
		return err
	}
	parsed := parseQuery(query)
	if parsed.responseShape == responseShapeUnsupported {
		return h.unsupported(parsed.unsupported)
	}
	// Only SELECT statements enter SQL cache analysis. Session commands and
	// mutations must still be proxied and tracked, but reporting them as
	// uncacheable query analyses obscures the classification of the SELECT
	// that follows (Grafana sends SET time_zone when opening a connection).
	if parsed.statementType != vtparser.StmtSelect {
		return h.proxyQuery(session, query, parsed, callback)
	}
	analysis := defaultAnalyzer.analyzeParsed(query, parsed.statement, parsed.err)
	h.observeAnalysis(parsed.statementType, analysis)
	if h.cacheEligible(session) && analysis.Mode != sqlanalyzer.CacheModeNone {
		cacheStarted := time.Now()
		result, cacheStatus, cacheErr := h.executeCached(c, session, query, analysis)
		if cacheErr != nil {
			h.observeCache(analysis.Mode, cachestatus.LookupStatusProxyError, 0,
				time.Since(cacheStarted))
			return cacheErr
		}
		if limitErr := h.validateResult(session, result); limitErr != nil {
			h.observeCache(analysis.Mode, cachestatus.LookupStatusProxyError, 0,
				time.Since(cacheStarted))
			return limitErr
		}
		// Cached results deliberately report no origin warnings, but retain
		// the status flags captured with the cached result.
		h.setProtocolState(session, result.StatusFlags, 0)
		h.observeCache(analysis.Mode, cacheStatus, len(result.Rows), time.Since(cacheStarted))
		return callback(result)
	}
	if analysis.Mode == sqlanalyzer.CacheModeNone {
		return h.proxyQuery(session, query, parsed, callback)
	}
	proxyStarted := time.Now()
	points := 0
	err = h.proxyQuery(session, query, parsed, func(result *sqltypes.Result) error {
		points += len(result.Rows)
		return callback(result)
	})
	cacheStatus := cachestatus.LookupStatusProxyOnly
	if err != nil {
		cacheStatus = cachestatus.LookupStatusProxyError
	}
	h.observeCache(analysis.Mode, cacheStatus, points, time.Since(proxyStarted))
	return err
}

func hasMultipleStatements(query string) (multiple bool, err error) {
	defer func() {
		if recover() != nil {
			multiple = false
			err = fmt.Errorf("%w: statement boundary parsing failed", ErrInvalidSQL)
		}
	}()
	pieces, err := defaultAnalyzer.parser.SplitStatementToPieces(query)
	return len(pieces) > 1, err
}

type parsedQuery struct {
	statementType vtparser.StatementType
	statement     vtparser.Statement
	err           error
	responseShape queryResponseShape
	unsupported   string
}

type queryResponseShape uint8

const (
	responseShapeUnknown queryResponseShape = iota
	responseShapeOK
	responseShapeRows
	responseShapeUnsupported
)

func parseQuery(query string) parsedQuery {
	parsed := parsedQuery{statementType: vtparser.Preview(query)}
	switch parsed.statementType {
	case vtparser.StmtSelect, vtparser.StmtUse, vtparser.StmtSet, vtparser.StmtUnknown:
		parsed.statement, parsed.err = defaultAnalyzer.parser.Parse(query)
		if parsed.err == nil && parsed.statementType == vtparser.StmtUnknown {
			// Preview classifies statements by their leading keyword, so WITH and
			// VALUES statements need the full AST to determine their behavior.
			parsed.statementType = vtparser.ASTToStatementType(parsed.statement)
			if _, ok := parsed.statement.(*vtparser.ValuesStatement); ok {
				// Vitess leaves a standalone VALUES statement unknown even though
				// MySQL treats it as a read that returns a result set.
				parsed.statementType = vtparser.StmtSelect
			}
		}
	}
	parsed.responseShape, parsed.unsupported = classifyResponseShape(query, parsed)
	return parsed
}

type queryTokenSummary struct {
	firstValue           string
	hasInto              bool
	explainInto          bool
	maintenancePartition bool
}

func summarizeQueryTokens(query string) queryTokenSummary {
	tokenizer := defaultAnalyzer.parser.NewStringTokenizer(query)
	var summary queryTokenSummary
	maintenance := false
	explainBody := false
	for {
		tokenType, value := tokenizer.Scan()
		if tokenType == 0 || tokenType == ';' || tokenType == vtparser.LEX_ERROR {
			return summary
		}
		if tokenType == vtparser.COMMENT {
			continue
		}
		if summary.firstValue == "" {
			summary.firstValue = strings.ToLower(value)
		}
		if tokenType == vtparser.INTO {
			summary.hasInto = true
			if !explainBody {
				summary.explainInto = true
			}
		}
		if tokenType == vtparser.PARTITION && maintenance {
			summary.maintenancePartition = true
		}
		switch tokenType {
		case vtparser.ANALYZE, vtparser.CHECK, vtparser.OPTIMIZE, vtparser.REPAIR:
			maintenance = true
		default:
			maintenance = false
		}
		switch tokenType {
		case vtparser.SELECT, vtparser.TABLE, vtparser.DELETE, vtparser.INSERT,
			vtparser.REPLACE, vtparser.UPDATE:
			explainBody = true
		}
	}
}

func classifyResponseShape(query string, parsed parsedQuery) (queryResponseShape, string) {
	// Prefer the AST whenever Vitess models the response distinction exactly.
	switch statement := parsed.statement.(type) {
	case *vtparser.Select:
		if statement.Into != nil {
			return responseShapeOK, ""
		}
		return responseShapeRows, ""
	case *vtparser.Union:
		if statement.Into != nil {
			return responseShapeOK, ""
		}
		return responseShapeRows, ""
	case *vtparser.ValuesStatement:
		return responseShapeRows, ""
	case *vtparser.PurgeBinaryLogs:
		return responseShapeOK, ""
	}

	switch parsed.statementType {
	case vtparser.StmtSelect:
		// A parser-rejected SELECT still has a predictable packet shape when
		// MySQL accepts syntax newer than Vitess's grammar.
		if summarizeQueryTokens(query).hasInto {
			return responseShapeOK, ""
		}
		return responseShapeRows, ""
	case vtparser.StmtShow, vtparser.StmtAnalyze:
		return responseShapeRows, ""
	case vtparser.StmtExplain:
		if summarizeQueryTokens(query).explainInto {
			return responseShapeOK, ""
		}
		return responseShapeRows, ""
	case vtparser.StmtOther:
		switch summarizeQueryTokens(query).firstValue {
		case "repair", "optimize":
			return responseShapeRows, ""
		case "do":
			return responseShapeOK, ""
		default:
			return responseShapeUnsupported, "unclassified administrative statements"
		}
	case vtparser.StmtDDL:
		if summarizeQueryTokens(query).maintenancePartition {
			return responseShapeRows, ""
		}
		return responseShapeOK, ""
	case vtparser.StmtUnknown:
		summary := summarizeQueryTokens(query)
		switch summary.firstValue {
		case "table":
			if summary.hasInto {
				return responseShapeOK, ""
			}
			return responseShapeRows, ""
		case "check", "checksum":
			return responseShapeRows, ""
		case "help":
			return responseShapeUnsupported, "HELP statements"
		case "xa":
			return responseShapeUnsupported, "XA statements"
		case "handler":
			return responseShapeUnsupported, "HANDLER statements"
		case "cache":
			return responseShapeUnsupported, "CACHE INDEX statements"
		default:
			return responseShapeUnsupported, "unclassified statements"
		}
	default:
		return responseShapeOK, ""
	}
}

func unsupportedTextFeature(query string, noBackslashEscapes bool) string {
	if hasExecutableComment(query, noBackslashEscapes) {
		return "versioned executable comments"
	}
	trimmed := strings.TrimSpace(vtparser.StripLeadingComments(query))
	if trimmed == "" {
		return ""
	}
	keyword := trimmed
	if end := strings.IndexAny(keyword, " \t\r\n\f\v"); end >= 0 {
		keyword = keyword[:end]
	}
	switch {
	case strings.EqualFold(keyword, "prepare"), strings.EqualFold(keyword, "execute"),
		strings.EqualFold(keyword, "deallocate"):
		return "prepared statements"
	case strings.EqualFold(keyword, "call"):
		return "stored procedures and multi-results"
	case strings.EqualFold(keyword, "load"):
		return "LOAD DATA and local-file operations"
	default:
		return ""
	}
}

func hasExecutableComment(query string, noBackslashEscapes bool) bool {
	// Keep the common path allocation-free; only text containing the executable
	// comment opener needs tokenization to distinguish comments from literals.
	if !strings.Contains(query, "/*!") {
		return false
	}
	if noBackslashEscapes && strings.Contains(query, `\`) {
		// Vitess always treats backslashes as string escapes. Pairing each one
		// makes it a literal for tokenization, matching NO_BACKSLASH_ESCAPES
		// quote boundaries without changing the query sent to the origin.
		query = strings.ReplaceAll(query, `\`, `\\`)
	}
	tokenizer := defaultAnalyzer.parser.NewStringTokenizer(query)
	tokenizer.SkipSpecialComments = true
	for {
		tokenType, value := tokenizer.Scan()
		if (tokenType == vtparser.COMMENT || tokenType == vtparser.LEX_ERROR) &&
			strings.HasPrefix(value, "/*!") {
			return true
		}
		if tokenType == 0 || tokenType == ';' || tokenType == vtparser.LEX_ERROR {
			return false
		}
	}
}

func (h *protocolHandler) proxyQuery(session *upstreamSession, query string, parsed parsedQuery,
	callback func(*sqltypes.Result) error,
) error {
	if err := h.connectSession(session); err != nil {
		return err
	}
	session.mtx.Lock()
	upstream := session.conn
	session.mtx.Unlock()
	if parsed.responseShape == responseShapeRows {
		err := h.runOriginQuery(session, upstream, parsed, func() error {
			return h.proxyResultSet(session, upstream, query, callback)
		})
		if err != nil {
			return err
		}
		h.updateSessionStateParsed(session, parsed)
		return nil
	}
	var result *sqltypes.Result
	var warnings uint16
	err := h.runOriginQuery(session, upstream, parsed, func() error {
		var executeErr error
		result, warnings, executeErr = upstream.ExecuteFetchWithWarningCount(query, 1, true)
		return executeErr
	})
	if err != nil {
		return err
	}
	h.setProtocolState(session, result.StatusFlags, warnings)
	h.updateSessionStateParsed(session, parsed)
	return callback(result)
}

func (h *protocolHandler) setProtocolState(session *upstreamSession, statusFlags,
	warnings uint16,
) {
	session.mtx.Lock()
	session.warnings = warnings
	if session.downstream != nil {
		session.downstream.StatusFlags = statusFlags
	}
	session.mtx.Unlock()
}

func originProtocolState(upstream *vtmysql.Conn) (uint16, uint16, error) {
	// FetchNext consumes but does not expose a streamed result's terminal EOF
	// metadata, so recover the connection status and diagnostic warning count.
	result, err := upstream.ExecuteFetch(warningCountQuery, 1, true)
	if err != nil {
		return 0, 0, err
	}
	if result == nil || len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return 0, 0, errors.New("invalid MySQL warning-count result")
	}
	warningCount, err := result.Rows[0][0].ToUint64()
	if err != nil {
		return 0, 0, fmt.Errorf("invalid MySQL warning count: %w", err)
	}
	if warningCount > uint64(^uint16(0)) {
		return result.StatusFlags, ^uint16(0), nil
	}
	return result.StatusFlags, uint16(warningCount), nil //nolint:gosec // range checked above
}

func (h *protocolHandler) proxyResultSet(session *upstreamSession, upstream *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if err := upstream.ExecuteStreamFetch(query); err != nil {
		// The origin rejected the statement before opening a result stream, so
		// it answered with a complete ERR packet and stayed synchronized.
		return err
	}
	// Every failure from here on leaves a result stream the origin is still
	// sending. CloseResult drains what it can but reports nothing, so Trickster
	// cannot prove the connection resynchronized and must not reuse it.
	if err := h.streamResultSet(session, upstream, callback); err != nil {
		// The stream is abandoned partway through, and CloseResult would
		// keep reading until the origin sends a remainder it may never send.
		// The connection is already unusable, so close it instead of draining.
		upstream.Close()
		return upstreamFatal{err: err}
	}
	upstream.CloseResult()
	return nil
}

func (h *protocolHandler) streamResultSet(session *upstreamSession, upstream *vtmysql.Conn,
	callback func(*sqltypes.Result) error,
) error {
	fields, err := upstream.Fields()
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		statusFlags, warnings, stateErr := originProtocolState(upstream)
		if stateErr != nil {
			return stateErr
		}
		h.setProtocolState(session, statusFlags, warnings)
		return callback(&sqltypes.Result{})
	}
	size, overflow := resultFieldsSize(fields, h.config.MaxResultSizeBytes)
	if overflow {
		return h.resultLimitExceeded(session)
	}
	rows := 0
	emitted := false
	batch := &sqltypes.Result{Fields: fields, Rows: make([][]sqltypes.Value, 0, resultBatchSize)}
	for {
		row, fetchErr := upstream.FetchNext(nil)
		if fetchErr != nil {
			if emitted && session.downstream != nil {
				session.downstream.MarkForClose()
			}
			return fetchErr
		}
		if row == nil {
			statusFlags, warnings, stateErr := originProtocolState(upstream)
			if stateErr != nil {
				if emitted && session.downstream != nil {
					session.downstream.MarkForClose()
				}
				return stateErr
			}
			h.setProtocolState(session, statusFlags, warnings)
			if len(batch.Rows) > 0 || batch.Fields != nil {
				if err := callback(batch); err != nil {
					if session.downstream != nil {
						session.downstream.MarkForClose()
					}
					return err
				}
				return nil
			}
			return nil
		}
		rows++
		if rows > h.config.MaxResultRows {
			return h.resultLimitExceeded(session)
		}
		size, overflow = addRowSize(size, row, h.config.MaxResultSizeBytes)
		if overflow {
			return h.resultLimitExceeded(session)
		}
		batch.Rows = append(batch.Rows, row)
		if len(batch.Rows) == resultBatchSize {
			emitted = true
			if err := callback(batch); err != nil {
				if session.downstream != nil {
					session.downstream.MarkForClose()
				}
				return err
			}
			batch = &sqltypes.Result{Rows: make([][]sqltypes.Value, 0, resultBatchSize)}
		}
	}
}

// runOriginQuery executes operation against upstream, then decides whether the
// connection survived. parsed describes the statement so any state a failure
// may have left behind is recorded before abandonUpstream weighs whether the
// session can still be reconnected.
func (h *protocolHandler) runOriginQuery(session *upstreamSession, upstream *vtmysql.Conn,
	parsed parsedQuery, operation func() error,
) error {
	ctx := session.traceContext
	var span trace.Span
	if h.config.Tracer != nil && h.config.Tracer.Tracer != nil {
		_, span = h.config.Tracer.Start(ctx, "mysql.origin.query",
			trace.WithAttributes(attribute.String("trickster.backend", h.config.BackendName)))
		defer span.End()
	}
	var timedOut atomic.Bool
	var timer *time.Timer
	var timerDone sync.WaitGroup
	if h.config.QueryTimeout > 0 {
		timerDone.Add(1)
		timer = time.AfterFunc(h.config.QueryTimeout, func() {
			defer timerDone.Done()
			timedOut.Store(true)
			upstream.Close()
		})
	}
	err := operation()
	if timer != nil {
		if timer.Stop() {
			timerDone.Done()
		}
		timerDone.Wait()
	}
	if timedOut.Load() {
		h.updateSessionStateFailed(session, parsed)
		h.abandonUpstream(session, upstream, "query_timeout")
		return sqlerror.NewSQLError(sqlerror.ERQueryInterrupted, sqlerror.SSQueryInterrupted,
			"MySQL origin query exceeded Trickster's configured timeout")
	}
	if err != nil {
		h.updateSessionStateFailed(session, parsed)
		if upstreamRetainable(upstream, err) {
			// An ordinary server error is a complete, synchronized response.
			// Keeping the connection preserves the transaction, temporary
			// objects, locks, and variables the client still expects.
			metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName,
				"origin_statement").Inc()
			return err
		}
		h.abandonUpstream(session, upstream, "origin_query")
		// The fatality marker is internal bookkeeping; the client still needs
		// the origin's own SQL error so Vitess can encode its errno.
		return originError(err)
	}
	return nil
}

func originError(err error) error {
	if fatal, ok := errors.AsType[upstreamFatal](err); ok {
		return fatal.err
	}
	return err
}

// upstreamFatal marks an error that left the origin connection unusable: a
// stream Trickster abandoned before the origin finished sending it, or any
// other locally known desynchronization.
type upstreamFatal struct{ err error }

func (e upstreamFatal) Error() string { return e.err.Error() }

func (e upstreamFatal) Unwrap() error { return e.err }

// upstreamRetainable reports whether err left the origin connection usable and
// synchronized, which is true only for an ordinary server SQL error.
func upstreamRetainable(upstream *vtmysql.Conn, err error) bool {
	if err == nil {
		return true
	}
	if upstream == nil || upstream.IsClosed() {
		return false
	}
	if _, ok := errors.AsType[upstreamFatal](err); ok {
		return false
	}
	var sqlErr *sqlerror.SQLError
	if !errors.As(err, &sqlErr) {
		// A non-protocol error carries no proof the exchange completed.
		return false
	}
	return !originConnectionFatal(sqlErr)
}

// originConnectionFatal reports whether err means the origin connection can no
// longer serve commands. Vitess's sqlerror.IsConnErr answers a different
// question and cannot be used directly: it counts ER_QUERY_INTERRUPTED, which
// KILL QUERY delivers as a complete ERR packet on a connection that stays
// usable, and it omits the server-terminal codes below.
func originConnectionFatal(err *sqlerror.SQLError) bool {
	switch err.Number() {
	case sqlerror.ERQueryInterrupted:
		// KILL QUERY ends the statement, not the connection.
		return false
	case sqlerror.ERServerShutdown, sqlerror.ERForcingClose, sqlerror.ERAbortingConnection:
		// The origin announced that it is dropping this connection.
		return true
	}
	return sqlerror.IsConnErr(err)
}

// abandonUpstream drops an origin connection Trickster can no longer use. A
// session whose state a reconnect cannot reproduce becomes terminal, so the
// client is told its session is gone instead of silently continuing on a fresh
// autocommit connection.
func (h *protocolHandler) abandonUpstream(session *upstreamSession, upstream *vtmysql.Conn,
	class string,
) {
	h.discardUpstream(session, upstream)
	metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, class).Inc()
	if session == nil {
		return
	}
	session.mtx.Lock()
	lost := session.inTx || session.stateful
	if lost {
		session.terminal = true
	}
	downstream := session.downstream
	session.mtx.Unlock()
	if !lost {
		return
	}
	if downstream != nil {
		downstream.MarkForClose()
	}
	metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName,
		"session_state_lost").Inc()
}

func resultFieldsSize(fields []*querypb.Field, limit int64) (int64, bool) {
	var size int64
	for _, field := range fields {
		if field == nil {
			continue
		}
		length := int64(len(field.Name) + len(field.Table) + len(field.Database))
		if length > limit-size {
			return limit, true
		}
		size += length
	}
	return size, false
}

func addRowSize(size int64, row []sqltypes.Value, limit int64) (int64, bool) {
	for _, value := range row {
		length := int64(value.Len())
		if length > limit-size {
			return limit, true
		}
		size += length
	}
	return size, false
}

func (h *protocolHandler) resultLimitExceeded(session *upstreamSession) error {
	metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "result_limit").Inc()
	if session.downstream != nil {
		session.downstream.MarkForClose()
	}
	return sqlerror.NewSQLError(sqlerror.ERNetPacketTooLarge, sqlerror.SSNetError,
		"MySQL result exceeds Trickster's configured limit")
}

func (h *protocolHandler) validateResult(session *upstreamSession, result *sqltypes.Result) error {
	if result == nil {
		return nil
	}
	if h.config.MaxResultRows > 0 && len(result.Rows) > h.config.MaxResultRows {
		return h.resultLimitExceeded(session)
	}
	if h.config.MaxResultSizeBytes <= 0 {
		return nil
	}
	size, overflow := resultFieldsSize(result.Fields, h.config.MaxResultSizeBytes)
	if overflow {
		return h.resultLimitExceeded(session)
	}
	for _, row := range result.Rows {
		size, overflow = addRowSize(size, row, h.config.MaxResultSizeBytes)
		if overflow {
			return h.resultLimitExceeded(session)
		}
	}
	return nil
}

func (h *protocolHandler) discardUpstream(session *upstreamSession, expected *vtmysql.Conn) {
	if session == nil {
		return
	}
	session.mtx.Lock()
	counted := false
	if session.conn == expected {
		session.conn = nil
		counted = session.upstreamCounted
		session.upstreamCounted = false
	}
	session.mtx.Unlock()
	if expected != nil {
		expected.Close()
	}
	if counted {
		h.activeUpstreams.Add(-1)
	}
}

func (h *protocolHandler) ComQueryMulti(_ *vtmysql.Conn, _ string,
	_ func(sqltypes.QueryResponse, bool, bool) error,
) error {
	return h.unsupported("multi-statements")
}

func (h *protocolHandler) ComPrepare(c *vtmysql.Conn, query string) ([]*querypb.Field, uint16, error) {
	if h.config.MaxQuerySizeBytes > 0 && len(query) > h.config.MaxQuerySizeBytes {
		c.MarkForClose()
		return nil, 0, sqlerror.NewSQLError(sqlerror.ERNetPacketTooLarge, sqlerror.SSNetError,
			"MySQL query exceeds Trickster's configured limit")
	}
	return nil, 0, h.unsupported("prepared statements")
}

func (h *protocolHandler) ComStmtExecute(_ *vtmysql.Conn, _ *vtmysql.PrepareData,
	_ func(*sqltypes.Result) error,
) error {
	return h.unsupported("prepared statements")
}

func (h *protocolHandler) ComRegisterReplica(_ *vtmysql.Conn, _ string, _ uint16, _, _ string) error {
	return h.unsupported("replica registration")
}

func (h *protocolHandler) ComBinlogDump(_ *vtmysql.Conn, _ string, _ uint32) error {
	return h.unsupported("binlog streaming")
}

func (h *protocolHandler) ComBinlogDumpGTID(_ *vtmysql.Conn, _ string, _ uint64,
	_ replication.GTIDSet, _ uint16,
) error {
	return h.unsupported("GTID binlog streaming")
}

func (h *protocolHandler) WarningCount(c *vtmysql.Conn) uint16 {
	h.mtx.Lock()
	session := h.sessions[c]
	h.mtx.Unlock()
	if session != nil {
		session.mtx.Lock()
		warnings := session.warnings
		session.mtx.Unlock()
		return warnings
	}
	return 0
}

func unsupported(feature string) error {
	return sqlerror.NewSQLErrorf(sqlerror.ERNotSupportedYet, sqlerror.SSClientError,
		"Trickster MySQL proxy does not yet support %s", feature)
}

func (h *protocolHandler) unsupported(feature string) error {
	metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "unsupported_command").Inc()
	return unsupported(feature)
}

func (h *protocolHandler) closeSession(c *vtmysql.Conn, final bool) {
	h.mtx.Lock()
	session := h.sessions[c]
	var upstream *vtmysql.Conn
	var counted, ready, forced bool
	var connectionSpan trace.Span
	if session != nil {
		session.mtx.Lock()
		upstream = session.conn
		counted = session.upstreamCounted
		ready = session.ready
		forced = session.forced
		connectionSpan = session.connectSpan
		session.conn = nil
		session.upstreamCounted = false
		session.connectSpan = nil
		session.warnings = 0
		session.mtx.Unlock()
	}
	if final {
		delete(h.sessions, c)
		delete(h.controls, c.ConnectionID)
	}
	h.mtx.Unlock()
	if upstream != nil {
		upstream.Close()
	}
	if counted {
		h.activeUpstreams.Add(-1)
	}
	if connectionSpan != nil {
		connectionSpan.End()
	}
	if final && session != nil {
		event := "clean_close"
		if forced {
			event = "forced_close"
		} else if !ready {
			event = "handshake_failure"
			metrics.MySQLConnectionErrors.WithLabelValues(h.config.BackendName, "handshake").Inc()
		}
		metrics.MySQLConnections.WithLabelValues(h.config.BackendName, event).Inc()
		metrics.MySQLActiveConnections.WithLabelValues(h.config.BackendName).Dec()
		h.wg.Done()
	}
}

func (h *protocolHandler) shutdown(ctx context.Context) error {
	h.closed.Store(true)
	h.mtx.Lock()
	for _, session := range h.sessions {
		session.mtx.Lock()
		session.forced = true
		if session.conn != nil {
			session.conn.Close()
			session.conn = nil
			if session.upstreamCounted {
				h.activeUpstreams.Add(-1)
				session.upstreamCounted = false
			}
		}
		session.mtx.Unlock()
	}
	h.mtx.Unlock()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		h.mtx.Lock()
		for downstream := range h.sessions {
			downstream.Close()
		}
		h.mtx.Unlock()
		return ctx.Err()
	}
}
