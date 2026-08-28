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
	"crypto/tls"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	authtypes "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/replication"
	"vitess.io/vitess/go/mysql/sqlerror"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/sqlparser"
	"vitess.io/vitess/go/vt/vtenv"
)

func validBackendOptions() *bo.Options {
	o := bo.New()
	o.Name = "mysql-test"
	o.OriginURL = "mysql://origin:origin-password@db.example/trickster"
	o.AuthenticatorName = "mysql-clients"
	o.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "client-password"},
	}
	return o
}

func TestMySQLBackendContract(t *testing.T) {
	o := validBackendOptions()
	backend, err := NewClient("mysql-test", o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*Client)
	if c.DefaultPathConfigs(o) != nil {
		t.Fatal("MySQL backend unexpectedly registered HTTP paths")
	}
	c.RegisterHandlers(nil)
	if c.DefaultHealthCheckConfig() == nil {
		t.Fatal("MySQL backend returned nil health-check defaults")
	}
	config, err := c.MySQLRouteConfig()
	if err != nil || config.BackendName != "mysql-test" {
		t.Fatalf("MySQLRouteConfig() = %+v, %v", config, err)
	}

	for _, tc := range []struct {
		name, host, want string
	}{
		{"nil", "", ""},
		{"default port", "db.example", "db.example:3306"},
		{"explicit port", "db.example:3307", "db.example:3307"},
		{"IPv6", "2001:db8::1", "[2001:db8::1]:3306"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options *bo.Options
			if tc.host != "" {
				options = bo.New()
				options.Host = tc.host
			}
			if got := OriginAddress(options); got != tc.want {
				t.Fatalf("OriginAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProtocolConfigurationErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(*bo.Options)
		want string
	}{
		{"nil options", nil, "nil MySQL backend options"},
		{"malformed URL", func(o *bo.Options) { o.OriginURL = "://bad" }, "parse MySQL origin URL"},
		{"wrong scheme", func(o *bo.Options) { o.OriginURL = "https://db.example" }, "unsupported MySQL origin scheme"},
		{"missing host", func(o *bo.Options) { o.OriginURL = "mysql://origin:password@/db" }, "no host"},
		{"invalid port", func(o *bo.Options) { o.OriginURL = "mysql://origin:password@db.example:bad/db" }, "invalid port"},
		{"missing username", func(o *bo.Options) { o.OriginURL = "mysql://db.example/db" }, "include a username"},
		{"multiple CAs", func(o *bo.Options) {
			o.TLS.CertificateAuthorityPaths = []string{"one.pem", "two.pem"}
		}, "one certificate_authority_path"},
		{"missing authenticator", func(o *bo.Options) { o.AuthenticatorName = "" }, "requires an authenticator_name"},
		{"observe only", func(o *bo.Options) { o.AuthOptions.ObserveOnly = true }, "cannot be observe_only"},
		{"no users", func(o *bo.Options) { o.AuthOptions.Users = nil }, "has no users"},
		{"empty username", func(o *bo.Options) {
			o.AuthOptions.Users = configtypes.EnvStringMap{"": "password"}
		}, "cannot be empty"},
		{"empty password", func(o *bo.Options) {
			o.AuthOptions.Users = configtypes.EnvStringMap{"client": ""}
		}, "cannot be empty"},
		{"password hash", func(o *bo.Options) {
			o.AuthOptions.Users = configtypes.EnvStringMap{"client": "$2a$10$abcdefghijklmnopqrstuv"}
		}, "requires a plaintext credential"},
		{"missing users file", func(o *bo.Options) {
			o.AuthOptions.UsersFile = "/definitely/not/a/mysql-users-file"
			o.AuthOptions.UsersFileFormat = authtypes.CSVNoHeader
		}, "load MySQL authenticator users"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.edit == nil {
				_, err := ProtocolConfigFromOptions(nil)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("ProtocolConfigFromOptions(nil) error = %v", err)
				}
				return
			}
			o := validBackendOptions()
			tc.edit(o)
			_, err := ProtocolConfigFromOptions(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ProtocolConfigFromOptions() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNativeListenerAdapterValidatesBackendLimits(t *testing.T) {
	o := validBackendOptions()
	o.MySQL = mo.New()
	o.MySQL.MaxResultRows = 0
	if err := (nativeListenerAdapter{}).ValidateBackend(o); err == nil ||
		!strings.Contains(err.Error(), "max_result_rows") {
		t.Fatalf("ValidateBackend() error = %v", err)
	}
}

func TestProtocolConfigurationHelpers(t *testing.T) {
	o := validBackendOptions()
	users, err := DownstreamCredentialsFromOptions(o)
	if err != nil || users["client"] != "client-password" {
		t.Fatalf("DownstreamCredentialsFromOptions() = %v, %v", users, err)
	}
	users["client"] = "changed"
	if o.AuthOptions.Users["client"] != "client-password" {
		t.Fatal("downstream credentials aliased configuration")
	}

	file := t.TempDir() + "/users.yaml"
	if err := os.WriteFile(file, []byte("file-client,file-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.AuthOptions.UsersFile = file
	o.AuthOptions.UsersFileFormat = authtypes.CSVNoHeader
	users, err = DownstreamCredentialsFromOptions(o)
	if err != nil || users["file-client"] != "file-password" {
		t.Fatalf("file credentials = %v, %v", users, err)
	}

	config, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	before := config.RestartKey
	config.ApplyListenerOptions(nil)
	if config.HandshakeTimeout <= 0 || config.RestartKey == before {
		t.Fatalf("listener defaults were not applied: %+v", config)
	}
	server, err := NewProtocolServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if server.ProtocolRestartKey() != config.RestartKey {
		t.Fatal("protocol restart key changed")
	}
	server.UpdateTLSConfig(&tls.Config{MinVersion: tls.VersionTLS13})
	server.UpdateRouteResolver(nil)
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "network timeout detail" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func TestClassifyMySQLHealthErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"deadline", context.DeadlineExceeded, "timed out"},
		{"canceled", context.Canceled, "timed out"},
		{"network timeout", timeoutNetError{}, "timed out"},
		{"SQL timeout", sqlerror.NewSQLError(sqlerror.ERUnknownError, "HY000", "query timeout"), "timed out"},
		{"SQL TLS text", sqlerror.NewSQLError(sqlerror.ERUnknownError, "HY000", "x509 failure"), "TLS handshake"},
		{"database access", sqlerror.NewSQLError(sqlerror.ERDBAccessDenied, "42000", "denied"), "authentication"},
		{"connection failure", sqlerror.NewSQLError(sqlerror.CRConnectionError, "HY000", "unreachable"), "connection failed"},
		{"generic refused", errors.New("dial: connection refused secret"), "refused"},
		{"generic TLS", errors.New("certificate secret"), "TLS handshake"},
		{"generic", errors.New("sensitive detail"), "mysql connect failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyHealthError("connect", tc.err)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("classifyHealthError() = %v", got)
				}
				return
			}
			if got == nil || !strings.Contains(got.Error(), tc.want) || strings.Contains(got.Error(), "secret") {
				t.Fatalf("classifyHealthError() = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestHealthProbeConfigurationFailure(t *testing.T) {
	backend, err := NewClient("mysql-test", validBackendOptions(), http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*Client)
	c.Configuration().OriginURL = "https://not-mysql.example"
	if err := c.HealthCheckProbe()(context.Background()); err == nil || !strings.Contains(err.Error(), "configuration is invalid") {
		t.Fatalf("HealthCheckProbe() error = %v", err)
	}
}

func TestRedactingVitessHandlerComposition(t *testing.T) {
	next := slog.NewTextHandler(&strings.Builder{}, nil)
	h := redactingVitessHandler{next: next}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("wrapped handler unexpectedly disabled info logging")
	}
	if h.WithAttrs([]slog.Attr{slog.String("key", "value")}) == nil {
		t.Fatal("WithAttrs returned nil")
	}
	if h.WithGroup("mysql") == nil {
		t.Fatal("WithGroup returned nil")
	}
}

func TestRejectedProtocolCommandMatrix(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-test"}, nil)
	c := &vtmysql.Conn{}
	if err := h.ComQueryMulti(c, "SELECT 1; SELECT 2", nil); err == nil {
		t.Fatal("multi-query unexpectedly succeeded")
	}
	if _, _, err := h.ComPrepare(c, "SELECT ?"); err == nil {
		t.Fatal("prepare unexpectedly succeeded")
	}
	h.config.MaxQuerySizeBytes = 1
	if _, _, err := h.ComPrepare(c, "SELECT ?"); err == nil || !c.IsMarkedForClose() {
		t.Fatal("oversized prepare did not close the connection")
	}
	h.config.MaxQuerySizeBytes = 0
	for name, call := range map[string]func() error{
		"execute":  func() error { return h.ComStmtExecute(c, nil, nil) },
		"register": func() error { return h.ComRegisterReplica(c, "host", 3306, "user", "password") },
		"binlog":   func() error { return h.ComBinlogDump(c, "binlog.000001", 4) },
		"gtid": func() error {
			return h.ComBinlogDumpGTID(c, "binlog.000001", 4, replication.GTIDSet(nil), 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("unsupported command unexpectedly succeeded")
			}
		})
	}
	if h.WarningCount(c) != 0 {
		t.Fatal("unknown session returned warnings")
	}
	h.sessions[c] = &upstreamSession{warnings: 7}
	if h.WarningCount(c) != 7 {
		t.Fatal("session warning count was not returned")
	}
}

func TestRoutedProtocolCommandDelegation(t *testing.T) {
	target := newProtocolHandler(ProtocolConfig{BackendName: "target"}, nil)
	h := &routedProtocolHandler{
		targets: map[string]*protocolHandler{"target": target}, controls: make(map[uint32]*phaseConn),
	}
	noRoute := &vtmysql.Conn{}
	if _, err := h.target(noRoute); err == nil {
		t.Fatal("missing route unexpectedly resolved")
	}
	if err := h.ComQuery(noRoute, "SELECT 1", nil); err == nil {
		t.Fatal("query without route unexpectedly succeeded")
	}
	if err := h.ComQueryMulti(noRoute, "SELECT 1", nil); err == nil {
		t.Fatal("multi-query without route unexpectedly succeeded")
	}
	if _, _, err := h.ComPrepare(noRoute, "SELECT ?"); err == nil {
		t.Fatal("prepare without route unexpectedly succeeded")
	}
	if err := h.ComStmtExecute(noRoute, nil, nil); err == nil {
		t.Fatal("execute without route unexpectedly succeeded")
	}
	if err := h.ComRegisterReplica(noRoute, "host", 3306, "user", "password"); err == nil {
		t.Fatal("register without route unexpectedly succeeded")
	}
	if err := h.ComBinlogDump(noRoute, "binlog", 4); err == nil {
		t.Fatal("binlog without route unexpectedly succeeded")
	}
	if err := h.ComBinlogDumpGTID(noRoute, "binlog", 4, nil, 0); err == nil {
		t.Fatal("GTID binlog without route unexpectedly succeeded")
	}
	if h.WarningCount(noRoute) != 0 {
		t.Fatal("missing route returned warnings")
	}
	h.ComResetConnection(noRoute)
	if !noRoute.IsMarkedForClose() {
		t.Fatal("reset without route did not close the connection")
	}
	h.ConnectionClosed(noRoute)

	routed := &vtmysql.Conn{ClientData: &routedConnection{target: target}}
	if err := h.ComQuery(routed, "SELECT 1", nil); err == nil {
		t.Fatal("delegated query without a target session unexpectedly succeeded")
	}
	if err := h.ComQueryMulti(routed, "SELECT 1; SELECT 2", nil); err == nil {
		t.Fatal("delegated multi-query unexpectedly succeeded")
	}
	if _, _, err := h.ComPrepare(routed, "SELECT ?"); err == nil {
		t.Fatal("delegated prepare unexpectedly succeeded")
	}
	if err := h.ComStmtExecute(routed, nil, func(*sqltypes.Result) error { return nil }); err == nil {
		t.Fatal("delegated execute unexpectedly succeeded")
	}
	if err := h.ComRegisterReplica(routed, "host", 3306, "user", "password"); err == nil {
		t.Fatal("delegated registration unexpectedly succeeded")
	}
	if err := h.ComBinlogDump(routed, "binlog", 4); err == nil {
		t.Fatal("delegated binlog unexpectedly succeeded")
	}
	if err := h.ComBinlogDumpGTID(routed, "binlog", 4, nil, 0); err == nil {
		t.Fatal("delegated GTID binlog unexpectedly succeeded")
	}
	if h.WarningCount(routed) != 0 {
		t.Fatal("delegated connection unexpectedly returned warnings")
	}
	h.ConnectionClosed(routed)
	reset := &vtmysql.Conn{ClientData: &routedConnection{target: target}}
	h.ComResetConnection(reset)
}

func TestProtocolSessionAdmissionAndShutdown(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-test", MaxUpstreamConnections: 1}, nil)
	if !h.reserveUpstream() || h.reserveUpstream() {
		t.Fatal("upstream admission limit was not enforced")
	}
	h.activeUpstreams.Add(-1)
	h.config.MaxUpstreamConnections = 0
	if !h.reserveUpstream() {
		t.Fatal("unlimited upstream admission failed")
	}
	h.activeUpstreams.Add(-1)

	c := &vtmysql.Conn{}
	if _, err := h.sessionState(c); err == nil {
		t.Fatal("missing session unexpectedly resolved")
	}
	h.NewConnection(c)
	if _, err := h.sessionState(c); err != nil {
		t.Fatal(err)
	}
	h.ConnectionReady(c)
	h.ComResetConnection(c)
	h.ConnectionClosed(c)
	if err := h.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := &vtmysql.Conn{}
	h.NewConnection(closed)
	if !closed.IsMarkedForClose() {
		t.Fatal("closed handler accepted a connection")
	}
}

type staticCacheProvider struct{ cache trickstercache.Cache }

func (p staticCacheProvider) Cache() trickstercache.Cache { return p.cache }

func TestCacheExecutionGuardBranches(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{ProxyOnly: true}, nil)
	if h.cacheEligible(&upstreamSession{}) {
		t.Fatal("proxy-only handler was cache eligible")
	}
	h.config.ProxyOnly = false
	if h.cacheEligible(nil) {
		t.Fatal("nil session was cache eligible")
	}
	cacheClient := newTestCache()
	h.config.CacheProvider = staticCacheProvider{cache: cacheClient}
	if h.cacheClient() != cacheClient {
		t.Fatal("cache provider was not preferred")
	}
	session := &upstreamSession{inTx: true}
	if h.cacheEligible(session) {
		t.Fatal("transaction was cache eligible")
	}
	session.inTx = false
	session.cacheUnsafe = true
	if h.cacheEligible(session) {
		t.Fatal("unsafe session was cache eligible")
	}
	session.cacheUnsafe = false
	if !h.cacheEligible(session) {
		t.Fatal("safe session was not cache eligible")
	}

	for _, analysis := range []sqlanalyzer.Analysis{
		{Mode: sqlanalyzer.CacheModeNone},
		{Mode: sqlanalyzer.CacheModeDelta},
	} {
		if _, _, err := h.executeCached(&vtmysql.Conn{}, session, "SELECT 1", analysis); err == nil {
			t.Fatalf("executeCached(%v) unexpectedly succeeded", analysis.Mode)
		}
	}
}

func TestDeltaWindowValidationAndFlooring(t *testing.T) {
	now := time.Unix(100, 500)
	valid := &sqlanalyzer.QueryPlan{
		Step:       time.Minute,
		LowerBound: &sqlanalyzer.Bound{Value: now, Inclusive: true},
		UpperBound: &sqlanalyzer.Bound{Value: now.Add(time.Minute), Inclusive: false},
	}
	invalid := []*sqlanalyzer.QueryPlan{
		nil,
		{},
		{Step: time.Minute, LowerBound: valid.LowerBound},
		{Step: 0, LowerBound: valid.LowerBound, UpperBound: valid.UpperBound},
		{Step: time.Minute,
			LowerBound: &sqlanalyzer.Bound{Value: now}, UpperBound: valid.UpperBound},
		{Step: time.Minute, LowerBound: valid.LowerBound,
			UpperBound: &sqlanalyzer.Bound{Value: now.Add(time.Minute), Inclusive: true}},
		{Step: time.Minute, LowerBound: valid.UpperBound, UpperBound: valid.LowerBound},
	}
	for i, plan := range invalid {
		if _, err := buildDeltaRequestWindow(plan); err == nil {
			t.Fatalf("invalid plan %d was accepted", i)
		}
	}
	window, err := buildDeltaRequestWindow(valid)
	if err != nil || !window.empty {
		t.Fatalf("short unaligned window = %+v, %v", window, err)
	}
	negative := time.Unix(-61, 500)
	if got := sqlanalyzer.FloorBucket(negative, time.Minute, 0); got.After(negative) || negative.Sub(got) >= time.Minute {
		t.Fatalf("negative floor = %v for %v", got, negative)
	}
}

func TestCacheEnvelopeFailureMatrix(t *testing.T) {
	if (&cachedQueryResult{size: 7}).Size() != 7 || (*cachedQueryResult)(nil).Size() != 0 {
		t.Fatal("cached result Size contract failed")
	}
	if _, err := marshalCachedQueryResult(nil); err == nil {
		t.Fatal("nil cache result encoded")
	}
	valid, err := marshalCachedQueryResult(&cachedQueryResult{result: &sqltypes.Result{}})
	if err != nil {
		t.Fatal(err)
	}
	badExtentCount := append([]byte(nil), valid...)
	binary.BigEndian.PutUint32(badExtentCount[9:13], 100)
	truncated := append([]byte(nil), cacheEnvelopeMagic[:]...)
	truncated = append(truncated, cacheEnvelopeVersion, 0, 0, 0, 0, 0, 0, 0, 1)
	badSize := append([]byte(nil), valid...)
	binary.BigEndian.PutUint32(badSize[13:17], 100)
	badProto := append([]byte(nil), valid[:17]...)
	badProto = append(badProto, 0xff)
	binary.BigEndian.PutUint32(badProto[13:17], 1)
	for name, data := range map[string][]byte{
		"extent count": badExtentCount,
		"truncated":    truncated,
		"result size":  badSize,
		"protobuf":     badProto,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalCachedQueryResult(data); err == nil {
				t.Fatal("invalid cache envelope decoded")
			}
		})
	}
}

func TestDeltaResultValidationMatrix(t *testing.T) {
	plan := &sqlanalyzer.QueryPlan{
		OutputColumn: "time", GroupColumns: []string{"metric"}, ValueColumns: []string{"value"},
		OutputUnit: timeseries.DateTimeUnixSecs,
	}
	fields := []*querypb.Field{
		{Name: "time", Type: querypb.Type_INT64},
		{Name: "metric", Type: querypb.Type_VARCHAR},
		{Name: "value", Type: querypb.Type_INT64},
	}
	if _, err := dpcTestHandler.mergeResults(nil, plan); err == nil {
		t.Fatal("empty merge succeeded")
	}
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{{Fields: fields}, nil}, plan); err == nil {
		t.Fatal("merge accepted nil part")
	}
	badFields := []*querypb.Field{{Name: "time", Type: querypb.Type_INT64}}
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{{Fields: fields}, {Fields: badFields}}, plan); err == nil {
		t.Fatal("merge accepted incompatible fields")
	}
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{{Fields: fields, Rows: [][]sqltypes.Value{{sqltypes.NewInt64(1)}}}}, plan); err == nil {
		t.Fatal("merge accepted short row")
	}
	badTime := &sqltypes.Result{Fields: fields, Rows: [][]sqltypes.Value{{
		sqltypes.NewVarChar("bad"), sqltypes.NewVarChar("m"), sqltypes.NewInt64(1),
	}}}
	if _, err := dpcTestHandler.mergeResults([]*sqltypes.Result{badTime}, plan); err == nil {
		t.Fatal("merge accepted invalid time")
	}
	if _, err := dpcTestHandler.cropAndSortResult(nil, plan, timeseries.Extent{}); err == nil {
		t.Fatal("nil crop succeeded")
	}
	if _, err := dpcTestHandler.cropAndSortResult(&sqltypes.Result{Fields: fields, Rows: [][]sqltypes.Value{{}}}, plan,
		timeseries.Extent{}); err == nil {
		t.Fatal("crop accepted short row")
	}

	for name, candidate := range map[string][]*querypb.Field{
		"duplicate time": {{Name: "time", Type: querypb.Type_INT64}, {Name: "time", Type: querypb.Type_INT64},
			{Name: "metric", Type: querypb.Type_VARCHAR}, {Name: "value", Type: querypb.Type_INT64}},
		"missing time":  {{Name: "metric", Type: querypb.Type_VARCHAR}, {Name: "value", Type: querypb.Type_INT64}},
		"missing group": {{Name: "time", Type: querypb.Type_INT64}, {Name: "value", Type: querypb.Type_INT64}},
		"missing value": {{Name: "time", Type: querypb.Type_INT64}, {Name: "metric", Type: querypb.Type_VARCHAR}},
		"text value": {{Name: "time", Type: querypb.Type_INT64}, {Name: "metric", Type: querypb.Type_VARCHAR},
			{Name: "value", Type: querypb.Type_VARCHAR}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resultIndexes(candidate, plan); err == nil {
				t.Fatal("invalid fields were accepted")
			}
		})
	}
	if compatibleFields(fields, fields[:1]) || compatibleFields([]*querypb.Field{nil}, fields[:1]) ||
		compatibleFields([]*querypb.Field{{Name: "x"}}, []*querypb.Field{{Name: "y"}}) {
		t.Fatal("incompatible field lists matched")
	}
	if _, err := resultEpoch(sqltypes.NewInt64(1), timeseries.FieldDataType(99)); err == nil {
		t.Fatal("unsupported time unit was accepted")
	}
}

func TestCacheProviderFailureBranches(t *testing.T) {
	configuration := cacheoptions.New()
	configuration.Name = "mysql-coverage-cache"
	configuration.Provider = "filesystem"
	cacheClient := newTestCache()
	cacheClient.configuration = configuration
	h := &protocolHandler{config: ProtocolConfig{Cache: cacheClient}}

	cacheClient.retrieveErr = errors.New("retrieve failure")
	if _, found := h.retrieveCached("key"); found {
		t.Fatal("failed retrieval returned a hit")
	}
	cacheClient.retrieveErr = nil
	encoded, err := marshalCachedQueryResult(&cachedQueryResult{result: &sqltypes.Result{}})
	if err != nil {
		t.Fatal(err)
	}
	cacheClient.data["large"] = encoded
	h.config.MaxObjectSize = 1
	if _, found := h.retrieveCached("large"); found {
		t.Fatal("oversized cached object returned a hit")
	}
	h.config.MaxObjectSize = 0
	cacheClient.removeErr = errors.New("remove failure")
	h.removeCached("key", "test")
	h.config.Cache = nil
	h.removeCached("key", "test")
	h.observeCacheFailure("test")
	if _, ok := memoryCacheClient(nil); ok {
		t.Fatal("nil cache identified as memory cache")
	}
	h.storeCached("nil", nil)
	h.storeCached("valid-without-cache", &cachedQueryResult{result: &sqltypes.Result{}})
	h.observeAnalysis(sqlparser.StmtSelect, sqlanalyzer.Analysis{})
}

func TestParserExpressionHelperMatrix(t *testing.T) {
	parser := defaultAnalyzer.parser
	parse := func(source string) sqlparser.Expr {
		t.Helper()
		expr, err := parser.ParseExpr(source)
		if err != nil {
			t.Fatal(err)
		}
		return expr
	}
	for _, tc := range []struct {
		expression         string
		safe, hasAggregate bool
	}{
		{"42", true, false},
		{"3.14", true, false},
		{"COUNT(*)", true, true},
		{"STDDEV(value)", false, false},
		{"COUNT(*) + 1", true, true},
		{"-SUM(value)", true, true},
		{"ROUND(COALESCE(SUM(value), 0), 2)", true, true},
		{"COALESCE(SUM(value), name)", false, false},
		{"ABS(SUM(value))", false, false},
		{"value", false, false},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			safe, aggregate := numericValueExpression(parse(tc.expression))
			if safe != tc.safe || aggregate != tc.hasAggregate {
				t.Fatalf("numericValueExpression() = %t/%t", safe, aggregate)
			}
		})
	}
	for _, tc := range []struct {
		expression string
		want       int64
		ok         bool
	}{
		{"+5", 5, true},
		{"-5", -5, true},
		{"'5'", 0, false},
		{"name", 0, false},
		{"~5", 0, false},
		{"9223372036854775808", 1<<63 - 1, false},
	} {
		got, ok := intLiteral(parse(tc.expression))
		if got != tc.want || ok != tc.ok {
			t.Fatalf("intLiteral(%q) = %d/%t", tc.expression, got, ok)
		}
	}
	outputs := []selectOutput{{alias: "bucket", sourceName: "time", sourceAxis: "events.time"},
		{sourceName: "value", sourceAxis: "events.value"}}
	for _, expression := range []string{"0", "3", "unknown", "value + 1"} {
		if _, ok := resolveOutputReference(parse(expression), outputs); ok {
			t.Fatalf("invalid output reference %q resolved", expression)
		}
	}
}

func TestParsedQueryAnalysisReusesUnmodifiedAST(t *testing.T) {
	parsed := parseQuery(safeDateTimeQuery)
	if parsed.err != nil || parsed.statement == nil {
		t.Fatalf("parseQuery() = %+v", parsed)
	}
	before := sqlparser.String(parsed.statement)
	analysis := defaultAnalyzer.analyzeParsed(safeDateTimeQuery, parsed.statement, parsed.err)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		t.Fatalf("analyzeParsed() = %+v", analysis)
	}
	if after := sqlparser.String(parsed.statement); after != before {
		t.Fatalf("analysis mutated parsed AST:\nbefore: %s\nafter:  %s", before, after)
	}

	session := &upstreamSession{}
	stateful := parseQuery("SELECT @tenant")
	(&protocolHandler{}).updateSessionStateParsed(session, stateful)
	if !session.cacheUnsafe {
		t.Fatal("parsed state-changing SELECT remained cache-safe")
	}
}

func TestColumnReferenceUsesStructuralAxis(t *testing.T) {
	parser := defaultAnalyzer.parser
	qualified, err := parser.ParseExpr("Analytics.Events.TS")
	if err != nil {
		t.Fatal(err)
	}
	name, axis, ok := columnReference(qualified)
	if !ok || name != "TS" || axis != "analytics\x00events\x00ts" {
		t.Fatalf("qualified column = %q/%q/%t", name, axis, ok)
	}
	quoted, err := parser.ParseExpr("`Analytics.Events.TS`")
	if err != nil {
		t.Fatal(err)
	}
	_, quotedAxis, ok := columnReference(quoted)
	if !ok || quotedAxis == axis {
		t.Fatalf("structurally distinct column axes collided: %q", quotedAxis)
	}
}

type emptyRangeRenderer struct{ err error }

func (r emptyRangeRenderer) RenderExtent(timeseries.Extent) (string, error) { return "", r.err }
func (r emptyRangeRenderer) renderTimeRange(time.Time, time.Time) (string, error) {
	return "SELECT 1 WHERE 1 = 0", r.err
}

type extentOnlyRenderer struct{}

func (extentOnlyRenderer) RenderExtent(timeseries.Extent) (string, error) { return "SELECT 1", nil }

func TestCacheOriginFallbackBranches(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-fallback", Cache: newTestCache()}, nil)
	c := &vtmysql.Conn{}
	session := &upstreamSession{}
	if _, _, err := h.executeCached(c, session, "SELECT 1", sqlanalyzer.Analysis{
		Mode: sqlanalyzer.CacheModeObject,
	}); err == nil {
		t.Fatal("object fallback without a session succeeded")
	}
	invalidPlan := &sqlanalyzer.QueryPlan{}
	if _, _, err := h.executeDelta(c, session, "SELECT 1", invalidPlan); err == nil {
		t.Fatal("invalid delta fallback without a session succeeded")
	}
	window := deltaRequestWindow{empty: true, lower: time.Unix(0, 0), upper: time.Unix(60, 0)}
	for _, renderer := range []sqlanalyzer.ExtentRenderer{
		extentOnlyRenderer{}, emptyRangeRenderer{err: errors.New("render failure")}, emptyRangeRenderer{},
	} {
		plan := &sqlanalyzer.QueryPlan{Renderer: renderer}
		if _, _, err := h.executeEmptyDelta(c, session, "SELECT 1", plan, window); err == nil {
			t.Fatal("empty delta fallback without a session succeeded")
		}
	}
	if _, err := h.executeOrigin(nil, "SELECT 1"); err == nil {
		t.Fatal("origin execution without a session succeeded")
	}
}

func TestCachedQueryClearsPriorWarningCount(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{
		BackendName: "mysql-warning-reset", Cache: newTestCache(),
	}, nil)
	c := &vtmysql.Conn{User: "client"}
	session := &upstreamSession{database: "trickster", warnings: 7, downstream: c}
	h.sessions[c] = session
	query := "SELECT 42"
	key := h.queryCacheKey(c, session, "opc", query)
	h.storeCached(key, &cachedQueryResult{result: &sqltypes.Result{
		Fields:      []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
		Rows:        [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
		StatusFlags: vtmysql.ServerStatusAutocommit,
	}})

	if err := h.ComQuery(c, query, func(*sqltypes.Result) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if warnings := h.WarningCount(c); warnings != 0 {
		t.Fatalf("cached query warning count = %d, want 0", warnings)
	}
	if c.StatusFlags != vtmysql.ServerStatusAutocommit {
		t.Fatalf("cached query status flags = %#x, want autocommit", c.StatusFlags)
	}
}

func TestProxyResultSetReturnsFinalCallbackErrorWithoutDownstream(t *testing.T) {
	origin := &testOriginHandler{env: vtenv.NewTestEnv()}
	params := startCoverageOrigin(t, origin)
	upstream, err := vtmysql.Connect(context.Background(), &params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upstream.Close)
	h := newProtocolHandler(ProtocolConfig{
		BackendName: "mysql-final-callback", MaxResultRows: 10, MaxResultSizeBytes: 1024,
	}, nil)
	want := errors.New("final callback failed")
	err = h.proxyResultSet(&upstreamSession{}, upstream, "SELECT 42",
		func(*sqltypes.Result) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("proxyResultSet() error = %v, want %v", err, want)
	}
}

func TestProtocolResultLimitHelperBranches(t *testing.T) {
	fields := []*querypb.Field{nil, {Name: "answer", Table: "results", Database: "db"}}
	if size, overflow := resultFieldsSize(fields, 100); overflow || size == 0 {
		t.Fatalf("resultFieldsSize() = %d/%t", size, overflow)
	}
	if _, overflow := resultFieldsSize(fields, 1); !overflow {
		t.Fatal("field-size overflow was not detected")
	}
	row := []sqltypes.Value{sqltypes.NewVarChar("abc"), sqltypes.NewInt64(42)}
	if _, overflow := addRowSize(0, row, 100); overflow {
		t.Fatal("small row overflowed")
	}
	if _, overflow := addRowSize(0, row, 1); !overflow {
		t.Fatal("row-size overflow was not detected")
	}
	h := newProtocolHandler(ProtocolConfig{}, nil)
	session := &upstreamSession{}
	if err := h.validateResult(session, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.validateResult(session, &sqltypes.Result{Rows: [][]sqltypes.Value{row}}); err != nil {
		t.Fatal(err)
	}
	h.config.MaxResultSizeBytes = 1
	if err := h.validateResult(session, &sqltypes.Result{Fields: fields}); err == nil {
		t.Fatal("oversized fields passed validation")
	}
	h.config.MaxResultSizeBytes = 4
	if err := h.validateResult(session, &sqltypes.Result{Rows: [][]sqltypes.Value{row}}); err == nil {
		t.Fatal("oversized row passed validation")
	}
	h.discardUpstream(nil, nil)
	h.discardUpstream(&upstreamSession{}, nil)
}

func TestCredentialAndRouteFailureBranches(t *testing.T) {
	salt := []byte("12345678901234567890")
	auth := newCredentialAuth(map[string]string{"client": "password"}, "mysql-router", nil)
	if _, err := auth.UserEntryWithHash(&vtmysql.Conn{}, salt, "missing", nil, nil); err == nil {
		t.Fatal("missing user authenticated")
	}
	bad := vtmysql.ScrambleMysqlNativePassword(salt, []byte("wrong"))
	if _, err := auth.UserEntryWithHash(&vtmysql.Conn{}, salt, "client", bad, nil); err == nil {
		t.Fatal("bad password authenticated")
	}
	noRoute := newCredentialAuth(map[string]string{"client": "password"}, "mysql-router", testRouteResolver{})
	response := vtmysql.ScrambleMysqlNativePassword(salt, []byte("password"))
	if _, err := noRoute.UserEntryWithHash(&vtmysql.Conn{}, salt, "client", response, nil); err == nil {
		t.Fatal("user without route authenticated")
	}

	for _, tc := range []struct {
		name     string
		resolver backends.RouteResolver
		targets  map[string]ProtocolConfig
		users    map[string]string
	}{
		{"nil resolver", nil, map[string]ProtocolConfig{"x": {}}, map[string]string{"u": "p"}},
		{"no targets", testRouteResolver{}, nil, map[string]string{"u": "p"}},
		{"no users", testRouteResolver{}, map[string]ProtocolConfig{"x": {}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRoutedProtocolServer(ProtocolConfig{DownstreamUsers: tc.users},
				tc.resolver, tc.targets); err == nil {
				t.Fatal("invalid routed server configuration succeeded")
			}
		})
	}
}

func TestDeltaCacheHitAndInvalidEntryBranches(t *testing.T) {
	cacheClient := newTestCache()
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-delta-coverage", Cache: cacheClient}, nil)
	c := &vtmysql.Conn{User: "client"}
	session := &upstreamSession{database: "trickster"}
	start := time.Unix(0, 0)
	end := time.Unix(120, 0)
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "SELECT delta", Step: time.Minute,
		LowerBound:   &sqlanalyzer.Bound{Value: start, Inclusive: true},
		UpperBound:   &sqlanalyzer.Bound{Value: end, Inclusive: false},
		OutputColumn: "time", ValueColumns: []string{"value"}, OutputUnit: timeseries.DateTimeUnixSecs,
		Renderer: emptyRangeRenderer{},
	}
	fields := []*querypb.Field{{Name: "time", Type: querypb.Type_INT64},
		{Name: "value", Type: querypb.Type_INT64}}
	result := &sqltypes.Result{Fields: fields, Rows: [][]sqltypes.Value{
		{sqltypes.NewInt64(0), sqltypes.NewInt64(1)},
		{sqltypes.NewInt64(60), sqltypes.NewInt64(2)},
	}}
	extent := timeseries.Extent{Start: start, End: time.Unix(60, 0)}
	key := h.queryCacheKey(c, session, "dpc", plan.CanonicalSQL, plan.IdentitySuffix)
	h.storeCached(key, &cachedQueryResult{result: result,
		extents: timeseries.ExtentList{extent}})
	got, status, err := h.executeDelta(c, session, "SELECT delta", plan)
	if err != nil || status != cachestatus.LookupStatusHit || len(got.Rows) != 2 {
		t.Fatalf("delta hit = %+v/%v/%v", got, status, err)
	}

	invalid := &sqltypes.Result{Fields: fields, Rows: [][]sqltypes.Value{
		{sqltypes.NewVarChar("bad-time"), sqltypes.NewInt64(1)},
	}}
	h.storeCached(key, &cachedQueryResult{result: invalid, extents: timeseries.ExtentList{extent}})
	if _, _, err := h.executeDelta(c, session, "SELECT delta", plan); err == nil {
		t.Fatal("invalid cached time axis did not fall back to the unavailable origin")
	}
	if _, found := h.retrieveCached(key); found {
		t.Fatal("invalid cached time axis was retained")
	}
	if _, _, _, err := h.finalizeDeltaResult(nil, nil, plan, extent, time.Now()); err == nil {
		t.Fatal("nil finalized result succeeded")
	}
}

func TestRetentionAndStableExtentGuardBranches(t *testing.T) {
	h := &protocolHandler{}
	plan := &sqlanalyzer.QueryPlan{Step: time.Minute, OutputColumn: "time",
		OutputUnit: timeseries.DateTimeUnixSecs}
	extents := timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(600, 0)}}
	result := &sqltypes.Result{Fields: []*querypb.Field{{Name: "other", Type: querypb.Type_INT64}},
		Rows: [][]sqltypes.Value{{sqltypes.NewInt64(1)}, {sqltypes.NewInt64(2)}}}
	if got, _, err := h.applyRetentionSorted(result, extents, plan, 0); err != nil || got != result {
		t.Fatal("disabled retention changed the result")
	}
	h.config.RetentionPoints = 1
	if _, _, err := h.applyRetentionSorted(result, extents, plan, 1); err == nil {
		t.Fatal("invalid retention row was accepted")
	}
	if got := h.stableExtents(extents, plan, time.Unix(1200, 0)); len(got) != 1 {
		t.Fatal("disabled backfill changed extents")
	}
	h.config.BackfillWindow = time.Minute
	if got := h.stableExtents(extents, plan, time.Unix(1200, 0)); len(got) != 1 {
		t.Fatal("expired backfill window changed extents")
	}
	if got := h.stableExtents(extents, plan, time.Unix(30, 0)); len(got) != 0 {
		t.Fatal("fully volatile extents were retained")
	}
}

func TestComQueryEarlyFailureMatrix(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-query-coverage", ProxyOnly: true}, nil)
	missing := &vtmysql.Conn{}
	if err := h.ComQuery(missing, "SELECT 1", nil); err == nil {
		t.Fatal("query without session succeeded")
	}
	c := &vtmysql.Conn{}
	h.sessions[c] = &upstreamSession{downstream: c}
	for _, query := range []string{
		"SELECT 1; SELECT 2",
		"PREPARE statement FROM 'SELECT 1'",
		"SELECT '",
		"SELECT 1",
	} {
		if err := h.ComQuery(c, query, func(*sqltypes.Result) error { return nil }); err == nil {
			t.Fatalf("query %q unexpectedly succeeded without an origin", query)
		}
	}
}

func TestRoutedConnectionSelectionFailures(t *testing.T) {
	h := &routedProtocolHandler{targets: make(map[string]*protocolHandler),
		controls: make(map[uint32]*phaseConn)}
	if _, ok := h.ResolveRoute(backends.RouteInput{}); ok {
		t.Fatal("nil resolver selected a route")
	}
	missingDecision := &vtmysql.Conn{}
	h.ConnectionReady(missingDecision)
	if !missingDecision.IsMarkedForClose() {
		t.Fatal("connection without a decision remained open")
	}

	options := bo.New()
	options.Name = "target"
	backend, err := backends.New("target", options, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingTarget := &vtmysql.Conn{ClientData: backends.RouteDecision{
		Target: backends.RouteTarget{Backend: backend},
	}}
	h.ConnectionReady(missingTarget)
	if !missingTarget.IsMarkedForClose() {
		t.Fatal("connection with an unknown target remained open")
	}

	target := newProtocolHandler(ProtocolConfig{BackendName: "target"}, nil)
	h.targets["target"] = target
	selected := &vtmysql.Conn{ClientData: backends.RouteDecision{
		Target: backends.RouteTarget{Backend: backend},
	}}
	h.ConnectionReady(selected)
	if _, ok := selected.ClientData.(*routedConnection); !ok {
		t.Fatal("selected connection did not retain its target")
	}
	h.NewConnection(selected)
}

func startCoverageOrigin(t *testing.T, handler vtmysql.Handler) vtmysql.ConnParams {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := vtmysql.NewFromListener(listener,
		newCredentialAuth(map[string]string{"origin": "password"}, "", nil), handler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go server.Accept()
	t.Cleanup(server.Shutdown)
	address := listener.Addr().(*net.TCPAddr)
	return vtmysql.ConnParams{Host: "127.0.0.1", Port: address.Port,
		Uname: "origin", Pass: "password"}
}

func TestShardedDeltaAndMergeFallback(t *testing.T) {
	deltaOrigin := &deltaOriginHandler{testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}}
	params := startCoverageOrigin(t, deltaOrigin)
	query := "SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time, COUNT(*) AS value " +
		"FROM events WHERE ts >= FROM_UNIXTIME(0) AND ts < FROM_UNIXTIME(300) " +
		"GROUP BY time ORDER BY time"
	analysis := defaultAnalyzer.Analyze(query, time.Time{})
	if analysis.Plan == nil {
		t.Fatalf("delta analysis failed: %+v", analysis)
	}
	h := newProtocolHandler(ProtocolConfig{
		BackendName: "mysql-sharded-coverage", Upstream: params, Cache: newTestCache(),
		DoesShard: true, ShardMaxPoints: 2, MaxResultRows: 100, MaxResultSizeBytes: 1 << 20,
	}, nil)
	c := &vtmysql.Conn{User: "client"}
	session := &upstreamSession{database: "trickster", downstream: c}
	result, status, err := h.executeDelta(c, session, query, analysis.Plan)
	if err != nil || status != cachestatus.LookupStatusKeyMiss || len(result.Rows) != 5 ||
		deltaOrigin.queryCount.Load() < 2 {
		t.Fatalf("sharded delta = %d rows/%v/%v, origin queries=%d", len(result.Rows), status, err,
			deltaOrigin.queryCount.Load())
	}
	h.discardUpstream(session, session.conn)

	emptyOrigin := &testOriginHandler{env: vtenv.NewTestEnv()}
	params = startCoverageOrigin(t, emptyOrigin)
	fallback := newProtocolHandler(ProtocolConfig{
		BackendName: "mysql-merge-fallback", Upstream: params, Cache: newTestCache(),
		MaxResultRows: 100, MaxResultSizeBytes: 1 << 20,
	}, nil)
	fallbackSession := &upstreamSession{database: "trickster", downstream: c}
	// A plan whose results cannot be merged degrades to the object cache.
	if result, status, err := fallback.executeDelta(c, fallbackSession, query,
		analysis.Plan); err != nil || status != cachestatus.LookupStatusKeyMiss || result == nil {
		t.Fatalf("merge fallback = %v/%v/%v, want an object-cache miss", result, status, err)
	}
	before := emptyOrigin.queryCount.Load()
	// The recorded fallback makes the next execution skip the delta attempt and
	// read the object entry the first one stored.
	if result, status, err := fallback.executeDelta(c, fallbackSession, query,
		analysis.Plan); err != nil || status != cachestatus.LookupStatusHit || result == nil {
		t.Fatalf("repeat merge fallback = %v/%v/%v, want an object-cache hit", result, status, err)
	}
	if got := emptyOrigin.queryCount.Load(); got != before {
		t.Fatalf("origin queries after the recorded fallback = %d, want %d", got, before)
	}
	fallback.discardUpstream(fallbackSession, fallbackSession.conn)
}

func TestShutdownTimeoutBranches(t *testing.T) {
	h := newProtocolHandler(ProtocolConfig{BackendName: "mysql-shutdown-coverage"}, nil)
	connection, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	c := vtmysql.NewConnForTest(connection)
	h.NewConnection(c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown() error = %v", err)
	}
	h.ConnectionClosed(c)

	first := newProtocolHandler(ProtocolConfig{BackendName: "first"}, nil)
	second := newProtocolHandler(ProtocolConfig{BackendName: "second"}, nil)
	firstConnection, firstPeer := net.Pipe()
	secondConnection, secondPeer := net.Pipe()
	t.Cleanup(func() {
		_ = firstPeer.Close()
		_ = secondPeer.Close()
	})
	firstConn := vtmysql.NewConnForTest(firstConnection)
	secondConn := vtmysql.NewConnForTest(secondConnection)
	first.NewConnection(firstConn)
	second.NewConnection(secondConn)
	routed := &routedProtocolHandler{targets: map[string]*protocolHandler{
		"first": first, "second": second,
	}}
	if err := routed.shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("routed shutdown() error = %v", err)
	}
	first.ConnectionClosed(firstConn)
	second.ConnectionClosed(secondConn)
}

func TestConfiguredHealthProbeFactory(t *testing.T) {
	o := validBackendOptions()
	o.OriginURL = "mysql://origin:password@127.0.0.1:1/trickster"
	backend, err := NewClient("mysql-health-coverage", o, http.NotFoundHandler(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := backend.(*Client).HealthCheckProbe()(ctx); err == nil {
		t.Fatal("unreachable configured health origin passed")
	}
}

var _ net.Error = timeoutNetError{}
var _ backends.Backend = (*Client)(nil)
