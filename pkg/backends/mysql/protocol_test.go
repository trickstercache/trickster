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
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	configtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/prometheus/client_golang/prometheus/testutil"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/replication"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/vtenv"
	"vitess.io/vitess/go/vt/vttls"
)

func TestProtocolConfigFromOptions(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://user:p%40ss@db.example:3307/my%20db"
	o.Timeout = 3_000_000_000
	o.AuthenticatorName = "mysql-clients"
	o.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "client-password"},
	}
	config, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if config.Upstream.Host != "db.example" || config.Upstream.Port != 3307 ||
		config.Upstream.Uname != "user" || config.Upstream.Pass != "p@ss" ||
		config.Upstream.DbName != "my db" ||
		config.DownstreamUsers["client"] != "client-password" {
		t.Fatalf("unexpected config: %+v", config.Upstream)
	}
}

func TestMaskUnsupportedCapabilities(t *testing.T) {
	serverVersion := []byte("8.0.0-test\x00")
	payloadSize := 1 + len(serverVersion) + 4 + 8 + 1 + 2 + 1 + 2 + 2
	packet := make([]byte, 4+payloadSize)
	packet[4] = 10
	copy(packet[5:], serverVersion)
	lower := 5 + len(serverVersion) + 4 + 8 + 1
	upper := lower + 2 + 1 + 2
	capabilities := uint32(vtmysql.CapabilityClientProtocol41 |
		vtmysql.CapabilityClientMultiStatements | vtmysql.CapabilityClientMultiResults)
	binary.LittleEndian.PutUint16(packet[lower:lower+2], uint16(capabilities))
	binary.LittleEndian.PutUint16(packet[upper:upper+2], uint16(capabilities>>16))

	masked := maskUnsupportedCapabilities(packet)
	got := uint32(binary.LittleEndian.Uint16(masked[lower:lower+2])) |
		uint32(binary.LittleEndian.Uint16(masked[upper:upper+2]))<<16
	if got&vtmysql.CapabilityClientProtocol41 == 0 ||
		got&(vtmysql.CapabilityClientMultiStatements|vtmysql.CapabilityClientMultiResults) != 0 {
		t.Fatalf("masked capabilities = %#x", got)
	}
	if capabilities == uint32(binary.LittleEndian.Uint16(packet[lower:lower+2]))|
		uint32(binary.LittleEndian.Uint16(packet[upper:upper+2]))<<16 {
		// The original pooled packet must remain unchanged.
		return
	}
	t.Fatal("capability masking mutated the input packet")
}

func TestQuerySizeLimitClosesConnection(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{BackendName: "mysql1", MaxQuerySizeBytes: 4}}
	c := &vtmysql.Conn{}
	err := h.ComQuery(c, "SELECT 1", func(*sqltypes.Result) error { return nil })
	if err == nil || !c.IsMarkedForClose() {
		t.Fatalf("oversized query error = %v, marked = %t", err, c.IsMarkedForClose())
	}
}

func TestResultLimitsCloseConnection(t *testing.T) {
	downstream := &vtmysql.Conn{}
	session := &upstreamSession{downstream: downstream}
	h := &protocolHandler{config: ProtocolConfig{
		BackendName: "mysql1", MaxResultRows: 1, MaxResultSizeBytes: 8,
	}}
	result := &sqltypes.Result{Rows: [][]sqltypes.Value{
		{sqltypes.NewVarBinary("1234")}, {sqltypes.NewVarBinary("5678")},
	}}
	if err := h.validateResult(session, result); err == nil || !downstream.IsMarkedForClose() {
		t.Fatalf("result limit error = %v, marked = %t", err, downstream.IsMarkedForClose())
	}
}

func TestProtocolRestartKeyIncludesTransportSettings(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://origin:password@db.example/database"
	o.AuthenticatorName = "mysql-auth"
	o.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "client-password"},
	}
	first, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Timeout++
	second, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if first.RestartKey == second.RestartKey {
		t.Fatal("timeout change did not alter MySQL protocol restart key")
	}
	o.TLS.InsecureSkipVerify = !o.TLS.InsecureSkipVerify
	third, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if second.RestartKey == third.RestartKey {
		t.Fatal("TLS change did not alter MySQL protocol restart key")
	}
	o.RequireTLS = true
	o.TLS.FullChainCertPath = "server.pem"
	o.TLS.PrivateKeyPath = "server-key.pem"
	fourth, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if third.RestartKey == fourth.RestartKey {
		t.Fatal("downstream TLS requirement did not alter MySQL protocol restart key")
	}
}

func TestProtocolRestartKeyIncludesTLSFileContents(t *testing.T) {
	caPath := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caPath, []byte("first certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := bo.New()
	o.OriginURL = "mysql://origin:password@db.example/database"
	o.AuthenticatorName = "mysql-auth"
	o.AuthOptions = &autho.Options{Users: configtypes.EnvStringMap{"client": "client-password"}}
	o.TLS.CertificateAuthorityPaths = []string{caPath}
	first, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, []byte("rotated certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := ProtocolConfigFromOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if first.RestartKey == second.RestartKey {
		t.Fatal("TLS file content rotation did not alter MySQL protocol restart key")
	}
}

func TestProtocolConfigRejectsIncompleteTLS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*bo.Options)
		want      string
	}{
		{
			name: "downstream certificate",
			configure: func(o *bo.Options) {
				o.TLS.FullChainCertPath = "server.pem"
			},
			want: "full_chain_cert_path and private_key_path",
		},
		{
			name: "upstream client certificate",
			configure: func(o *bo.Options) {
				o.TLS.ClientCertPath = "client.pem"
			},
			want: "client_cert_path and client_key_path",
		},
		{
			name: "required downstream TLS without certificate",
			configure: func(o *bo.Options) {
				o.RequireTLS = true
			},
			want: "require_tls requires",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := bo.New()
			o.OriginURL = "mysql://origin:password@db.example/database"
			o.AuthenticatorName = "mysql-clients"
			o.AuthOptions = &autho.Options{
				Users: configtypes.EnvStringMap{"client": "client-password"},
			}
			tc.configure(o)
			_, err := ProtocolConfigFromOptions(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ProtocolConfigFromOptions() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigureUpstreamTLSModes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		configure     func(*bo.Options)
		mode          vttls.SslMode
		ca, cert, key string
	}{
		{name: "disabled", mode: vttls.Disabled},
		{name: "required without verification", configure: func(o *bo.Options) {
			o.TLS.InsecureSkipVerify = true
		}, mode: vttls.Required},
		{name: "CA and hostname verified", configure: func(o *bo.Options) {
			o.TLS.CertificateAuthorityPaths = []string{"ca.pem"}
		}, mode: vttls.VerifyIdentity, ca: "ca.pem"},
		{name: "mutual TLS", configure: func(o *bo.Options) {
			o.TLS.ClientCertPath = "client.pem"
			o.TLS.ClientKeyPath = "client-key.pem"
		}, mode: vttls.VerifyIdentity, cert: "client.pem", key: "client-key.pem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := bo.New()
			if tc.configure != nil {
				tc.configure(o)
			}
			params := &vtmysql.ConnParams{SslMode: vttls.Disabled}
			configureUpstreamTLS(params, o)
			if params.SslMode != tc.mode || params.SslCa != tc.ca ||
				params.SslCert != tc.cert || params.SslKey != tc.key {
				t.Fatalf("TLS params = %#v", params)
			}
		})
	}
}

func TestProtocolServerRequiresInboundTLSConfig(t *testing.T) {
	server, err := NewProtocolServer(ProtocolConfig{RequireSecureTransport: true})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := server.Serve(listener); err == nil || !strings.Contains(err.Error(), "inbound TLS") {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestHasMultipleStatements(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{query: "SELECT 1", want: false},
		{query: "SELECT 1;", want: false},
		{query: "SELECT ';'", want: false},
		{query: "SELECT 1; SELECT 2", want: true},
	} {
		got, err := hasMultipleStatements(tc.query)
		if err != nil {
			t.Fatalf("hasMultipleStatements(%q): %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("hasMultipleStatements(%q) = %t, want %t", tc.query, got, tc.want)
		}
	}
}

func TestStatementBoundaryParserPanicReturnsParseError(t *testing.T) {
	query := "\"\\;0"
	if multiple, err := hasMultipleStatements(query); multiple || !errors.Is(err, ErrInvalidSQL) {
		t.Fatalf("hasMultipleStatements(%q) = %t, %v; want bounded invalid-SQL error",
			query, multiple, err)
	}
	err := (&protocolHandler{}).ComQuery(&vtmysql.Conn{}, query, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot parse MySQL statement boundaries") {
		t.Fatalf("ComQuery(%q) error = %v, want statement-boundary parse error", query, err)
	}
}

func TestUnsupportedTextFeature(t *testing.T) {
	for _, tc := range []struct {
		query              string
		noBackslashEscapes bool
		want               string
	}{
		{query: "SELECT 1"},
		{query: "/* Grafana */ SELECT 1"},
		{query: "/* ordinary */ /*!80000 SET @tenant = 1 */", want: "versioned executable comments"},
		{query: "SELECT 1 /*!80400 INTO @answer */", want: "versioned executable comments"},
		{query: "SELECT /*!80400 @tenant := 1, */ 1", want: "versioned executable comments"},
		{query: "SELECT '/*!80400 INTO @answer */'"},
		{query: "SELECT \"/*!80400 INTO @answer */\""},
		{query: `SELECT 'x\' /*!80400 INTO @answer */`},
		{query: `SELECT 'x\' /*!80400 INTO @answer */`, noBackslashEscapes: true,
			want: "versioned executable comments"},
		{query: `SELECT 'it''s /*!80400 INTO @answer */'`, noBackslashEscapes: true},
		{query: "SELECT `/*!80400 INTO @answer */`", noBackslashEscapes: true},
		{query: "SELECT 1 /* ordinary /*!80400 INTO @answer */", noBackslashEscapes: true},
		{query: "SELECT 1 # /*!80400 INTO @answer */", noBackslashEscapes: true},
		{query: "SELECT 1 -- /*!80400 INTO @answer */", noBackslashEscapes: true},
		{query: "PREPARE statement FROM 'SELECT 1'", want: "prepared statements"},
		{query: "EXECUTE statement", want: "prepared statements"},
		{query: "DEALLOCATE PREPARE statement", want: "prepared statements"},
		{query: "\t\nEXECUTE\tstatement", want: "prepared statements"},
		{query: "CALL report()", want: "stored procedures and multi-results"},
		{query: "LOAD DATA LOCAL INFILE '/tmp/data' INTO TABLE events", want: "LOAD DATA and local-file operations"},
	} {
		if got := unsupportedTextFeature(tc.query, tc.noBackslashEscapes); got != tc.want {
			t.Errorf("unsupportedTextFeature(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestExecutableCommentDetectionHonorsNoBackslashEscapes(t *testing.T) {
	const query = `SELECT 'x\' /*!80400 INTO @answer */`
	c := &vtmysql.Conn{StatusFlags: vtmysql.ServerStatusAutocommit |
		vtmysql.ServerStatusNoBackslashEscapes}
	err := (&protocolHandler{}).ComQuery(c, query, nil)
	if err == nil || !strings.Contains(err.Error(), "versioned executable comments") {
		t.Fatalf("ComQuery() error = %v, want executable-comment rejection", err)
	}
}

func TestResponseShapeClassification(t *testing.T) {
	for _, tc := range []struct {
		query       string
		wantShape   queryResponseShape
		unsupported string
	}{
		{query: "SELECT 1", wantShape: responseShapeRows},
		{query: "SELECT 'INTO'", wantShape: responseShapeRows},
		{query: "SELECT 1 INTO @answer", wantShape: responseShapeOK},
		{query: "VALUES ROW(1), ROW(2)", wantShape: responseShapeRows},
		{query: "TABLE trips", wantShape: responseShapeRows},
		{query: "TABLE trips ORDER BY 'INTO'", wantShape: responseShapeRows},
		{query: "TABLE trips LIMIT 1 INTO @trip", wantShape: responseShapeOK},
		{query: "ANALYZE TABLE trips", wantShape: responseShapeRows},
		{query: "CHECK TABLE trips", wantShape: responseShapeRows},
		{query: "CHECKSUM TABLE trips, fares", wantShape: responseShapeRows},
		{query: "OPTIMIZE TABLE trips", wantShape: responseShapeRows},
		{query: "REPAIR TABLE trips", wantShape: responseShapeRows},
		{query: "ALTER TABLE trips CHECK PARTITION p0", wantShape: responseShapeRows},
		{query: "ALTER TABLE trips ANALYZE PARTITION p0", wantShape: responseShapeRows},
		{query: "ALTER TABLE trips OPTIMIZE PARTITION p0", wantShape: responseShapeRows},
		{query: "ALTER TABLE trips REPAIR PARTITION p0", wantShape: responseShapeRows},
		{query: "ALTER TABLE trips ADD COLUMN fare int", wantShape: responseShapeOK},
		{query: "EXPLAIN SELECT 'INTO'", wantShape: responseShapeRows},
		{query: "EXPLAIN INSERT INTO trips VALUES (1)", wantShape: responseShapeRows},
		{query: "EXPLAIN FORMAT=JSON INTO @plan SELECT * FROM trips", wantShape: responseShapeOK},
		{query: "DO 1", wantShape: responseShapeOK},
		{query: "PURGE BINARY LOGS BEFORE '2026-01-01'", wantShape: responseShapeOK},
		{query: "HELP 'SELECT'", wantShape: responseShapeUnsupported,
			unsupported: "HELP statements"},
		{query: "XA RECOVER", wantShape: responseShapeUnsupported,
			unsupported: "XA statements"},
		{query: "HANDLER trips READ FIRST LIMIT 2", wantShape: responseShapeUnsupported,
			unsupported: "HANDLER statements"},
		{query: "CACHE INDEX trips IN hot_cache", wantShape: responseShapeUnsupported,
			unsupported: "CACHE INDEX statements"},
		{query: "RESET REPLICA", wantShape: responseShapeUnsupported,
			unsupported: "unclassified statements"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			parsed := parseQuery(tc.query)
			if parsed.responseShape != tc.wantShape || parsed.unsupported != tc.unsupported {
				t.Fatalf("parseQuery() shape = %d, %q; want %d, %q",
					parsed.responseShape, parsed.unsupported, tc.wantShape, tc.unsupported)
			}
		})
	}
}

func TestCacheIdentityIncludesAuthorizationAndSafeSessionState(t *testing.T) {
	h := &protocolHandler{config: ProtocolConfig{
		BackendName: "mysql1", CacheKeyPrefix: "release-contract",
	}}
	baseSession := &upstreamSession{database: "analytics", timeZone: "+00:00"}
	base := h.queryCacheKey(&vtmysql.Conn{User: "alice"}, baseSession, "opc", "SELECT 1")

	for _, tc := range []struct {
		name     string
		user     string
		db       string
		timeZone string
	}{
		{name: "username", user: "bob", db: "analytics", timeZone: "+00:00"},
		{name: "database", user: "alice", db: "reporting", timeZone: "+00:00"},
		{name: "time zone", user: "alice", db: "analytics", timeZone: "-07:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &upstreamSession{database: tc.db, timeZone: tc.timeZone}
			got := h.queryCacheKey(&vtmysql.Conn{User: tc.user}, session, "opc", "SELECT 1")
			if got == base {
				t.Fatalf("cache key did not change with %s", tc.name)
			}
		})
	}
}

func TestCacheIdentityEncodingIsUnambiguousAndVersioned(t *testing.T) {
	left := &protocolHandler{config: ProtocolConfig{
		BackendName: "mysql.one", CacheKeyPrefix: "shared",
	}}
	right := &protocolHandler{config: ProtocolConfig{
		BackendName: "mysql", CacheKeyPrefix: "one.shared",
	}}
	leftKey := left.queryCacheKey(&vtmysql.Conn{User: "alice\x00reporting"},
		&upstreamSession{database: "warehouse", timeZone: "+00:00"}, "opc", "SELECT 1")
	rightKey := right.queryCacheKey(&vtmysql.Conn{User: "alice"},
		&upstreamSession{database: "reporting\x00warehouse", timeZone: "+00:00"}, "opc", "SELECT 1")
	if leftKey == rightKey {
		t.Fatal("distinct structured cache identities collided")
	}
}

func TestSessionCacheSafetyContract(t *testing.T) {
	h := &protocolHandler{}

	session := &upstreamSession{database: "initial"}
	h.updateSessionStateParsed(session, parseQuery("USE reporting"))
	if session.database != "reporting" || session.upstream.DbName != "reporting" ||
		!session.upstreamParamsReady || session.cacheUnsafe {
		t.Fatalf("USE state = %+v", session)
	}
	h.updateSessionStateParsed(session, parseQuery("SET SESSION time_zone = '+00:00'"))
	if session.timeZone != "+00:00" || session.cacheUnsafe {
		t.Fatalf("time-zone state = %+v", session)
	}

	for _, query := range []string{
		"SET NAMES utf8mb4",
		"SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		"SET character_set_client = utf8mb4",
		"SET character_set_connection = utf8mb4",
		"SET character_set_results = utf8mb4",
		"SET collation_connection = utf8mb4_0900_ai_ci",
		"SET sql_mode = 'ANSI'",
		"SET @tenant = 1",
		"SELECT @tenant := 1",
		"SELECT @tenant",
		"SELECT GET_LOCK('reporting', 1)",
		"INSERT INTO events VALUES (1)",
		"CREATE TEMPORARY TABLE local_events (id bigint)",
		"LOCK TABLES events READ",
	} {
		t.Run(query, func(t *testing.T) {
			session := &upstreamSession{}
			h.updateSessionStateParsed(session, parseQuery(query))
			if !session.cacheUnsafe {
				t.Fatalf("%q did not make the session cache-unsafe", query)
			}
		})
	}

	transaction := &upstreamSession{}
	h.updateSessionStateParsed(transaction, parseQuery("BEGIN"))
	if !transaction.inTx || transaction.cacheUnsafe {
		t.Fatalf("BEGIN state = %+v", transaction)
	}
	h.updateSessionStateParsed(transaction, parseQuery("COMMIT"))
	if transaction.inTx || transaction.cacheUnsafe {
		t.Fatalf("COMMIT state = %+v", transaction)
	}

	h.updateSessionStateParsed(transaction, parseQuery("START TRANSACTION"))
	h.updateSessionStateParsed(transaction, parseQuery("SAVEPOINT before_report"))
	h.updateSessionStateParsed(transaction, parseQuery("ROLLBACK TO SAVEPOINT before_report"))
	if !transaction.inTx || transaction.cacheUnsafe {
		t.Fatalf("ROLLBACK TO SAVEPOINT state = %+v", transaction)
	}
	h.updateSessionStateParsed(transaction, parseQuery("RELEASE SAVEPOINT before_report"))
	if !transaction.inTx || transaction.cacheUnsafe {
		t.Fatalf("RELEASE SAVEPOINT state = %+v", transaction)
	}
	h.updateSessionStateParsed(transaction, parseQuery("ROLLBACK"))
	if transaction.inTx || transaction.cacheUnsafe {
		t.Fatalf("ROLLBACK state = %+v", transaction)
	}
}

func TestDownstreamFoundRowsCapabilityControlsSessionUpstream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability uint32
		want       bool
	}{
		{name: "set", capability: vtmysql.CapabilityClientFoundRows, want: true},
		{name: "unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProtocolHandler(ProtocolConfig{Upstream: vtmysql.ConnParams{
				Flags: uint64(vtmysql.CapabilityClientFoundRows | vtmysql.CapabilityClientLongFlag),
			}}, nil)
			c := &vtmysql.Conn{Capabilities: tc.capability}
			h.NewConnection(c)
			h.ConnectionReady(c)
			h.mtx.Lock()
			session := h.sessions[c]
			h.mtx.Unlock()
			session.mtx.Lock()
			flags := session.upstream.Flags
			session.mtx.Unlock()
			got := flags&uint64(vtmysql.CapabilityClientFoundRows) != 0
			if got != tc.want {
				t.Fatalf("upstream CLIENT_FOUND_ROWS = %t, want %t", got, tc.want)
			}
			if flags&uint64(vtmysql.CapabilityClientLongFlag) == 0 {
				t.Fatal("unrelated configured upstream flag was cleared")
			}
			if c.StatusFlags != vtmysql.ServerStatusAutocommit {
				t.Fatalf("initial downstream status = %#x, want autocommit", c.StatusFlags)
			}
			h.ConnectionClosed(c)
		})
	}
	routed := &routedProtocolHandler{}
	c := &vtmysql.Conn{}
	routed.NewConnection(c)
	if c.StatusFlags != vtmysql.ServerStatusAutocommit {
		t.Fatalf("routed initial downstream status = %#x, want autocommit", c.StatusFlags)
	}
}

func TestProtocolConfigRejectsMissingCredentials(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://db.example/database"
	o.AuthenticatorName = "mysql-clients"
	o.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "client-password"},
	}
	_, err := ProtocolConfigFromOptions(o)
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("ProtocolConfigFromOptions() error = %v", err)
	}
}

func TestProtocolConfigRejectsMissingAuthenticator(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://origin:password@db.example/database"
	_, err := ProtocolConfigFromOptions(o)
	if err == nil || !strings.Contains(err.Error(), "authenticator_name") {
		t.Fatalf("ProtocolConfigFromOptions() error = %v", err)
	}
}

func TestProtocolConfigRejectsHashedAuthenticatorCredential(t *testing.T) {
	o := bo.New()
	o.OriginURL = "mysql://origin:password@db.example/database"
	o.AuthenticatorName = "mysql-clients"
	o.AuthOptions = &autho.Options{
		Users: configtypes.EnvStringMap{"client": "$2a$hashed-password"},
	}
	_, err := ProtocolConfigFromOptions(o)
	if err == nil || !strings.Contains(err.Error(), "plaintext credential") {
		t.Fatalf("ProtocolConfigFromOptions() error = %v", err)
	}
}

func TestProtocolServerProxiesTextQueries(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &testOriginHandler{env: vtenv.NewTestEnv()}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{
			Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password",
		},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second,
		BackendName:     "mysql-test",
		Cache:           newTestCache(),
		CacheTTL:        time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()

	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	initializingClient, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
		DbName: "analytics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := initializingClient.Ping(); err != nil {
		t.Fatal(err)
	}
	initializingClient.Close()
	originHandler.queryCount.Store(0)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port,
		Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "42" {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil || len(result.Rows) != 1 || originHandler.queryCount.Load() != 1 {
		t.Fatalf("cached query result = %+v, err = %v, origin queries = %d",
			result, err, originHandler.queryCount.Load())
	}
	if _, err = client.ExecuteFetch("SET time_zone = 'UTC'", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if got := originHandler.queryCount.Load(); got != 3 {
		t.Fatalf("time-zone-scoped cache origin queries = %d, want 3", got)
	}
	if _, err = client.ExecuteFetch("SET @trickster_test = 1", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	proxyOnly := metrics.SQLQueryCache.WithLabelValues("mysql-test", mysqlDialect,
		sqlanalyzer.CacheModeObject.String(), status.LookupStatusProxyOnly.String())
	proxyOnlyBefore := testutil.ToFloat64(proxyOnly)
	if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(proxyOnly); got != proxyOnlyBefore+1 {
		t.Fatalf("proxy-only cache outcomes = %v, want %v", got, proxyOnlyBefore+1)
	}
	if got := originHandler.queryCount.Load(); got != 5 {
		t.Fatalf("session-state bypass origin queries = %d, want 5", got)
	}
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("protocol server did not stop")
	}
}

type testRouteResolver map[string]backends.RouteTarget

func (r testRouteResolver) ResolveRoute(input backends.RouteInput) (backends.RouteDecision, bool) {
	target, ok := r[input.Username]
	if !ok || !target.Available() {
		return backends.RouteDecision{}, false
	}
	return backends.RouteDecision{Target: target, Outcome: backends.RouteOutcomeSelected}, true
}

func TestRoutedProtocolServerSelectsNativeTargetPerConnection(t *testing.T) {
	startOrigin := func(marker, username, password string) (vtmysql.ConnParams, func()) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		handler := &routeOriginHandler{
			testOriginHandler: &testOriginHandler{env: vtenv.NewTestEnv()}, marker: marker,
		}
		server, err := vtmysql.NewFromListener(listener,
			newCredentialAuth(map[string]string{username: password}, "", nil), handler,
			0, 0, false, false, 0, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		go server.Accept()
		address := listener.Addr().(*net.TCPAddr)
		return vtmysql.ConnParams{
			Host: "127.0.0.1", Port: address.Port,
			Uname: username, Pass: password,
		}, server.Shutdown
	}

	upstreamA, stopA := startOrigin("a", "origin-a", "origin-a-password")
	defer stopA()
	upstreamB, stopB := startOrigin("b", "origin-b", "origin-b-password")
	defer stopB()
	newTarget := func(name string) backends.Backend {
		opts := bo.New()
		opts.Name = name
		opts.Provider = providers.MySQL
		opts.OriginURL = "http://example.com"
		backend, err := backends.New(name, opts, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return backend
	}
	targetA, targetB := newTarget("mysql-a"), newTarget("mysql-b")
	resolver := testRouteResolver{
		"alice": {Backend: targetA},
		"bob":   {Backend: targetB},
	}
	server, err := NewRoutedProtocolServer(ProtocolConfig{
		BackendName: "mysql-users", ConnectTimeout: time.Second,
		DownstreamUsers: map[string]string{
			"alice": "alice-password", "bob": "bob-password",
		},
	}, resolver, map[string]ProtocolConfig{
		"mysql-a": {
			BackendName: "mysql-a", Upstream: upstreamA,
			DownstreamUsers: map[string]string{"alice": "alice-password"},
			ConnectTimeout:  time.Second,
		},
		"mysql-b": {
			BackendName: "mysql-b", Upstream: upstreamB,
			DownstreamUsers: map[string]string{"bob": "bob-password"},
			ConnectTimeout:  time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	proxyAddress := proxyListener.Addr().(*net.TCPAddr)
	routeMetric := metrics.MySQLRouteSelections.WithLabelValues(
		"mysql-users", "mysql-a", string(backends.RouteOutcomeSelected))
	routeMetricBefore := testutil.ToFloat64(routeMetric)

	for _, tc := range []struct{ username, password, marker string }{
		{"alice", "alice-password", "a"},
		{"bob", "bob-password", "b"},
	} {
		client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
			Host: "127.0.0.1", Port: proxyAddress.Port, Uname: tc.username, Pass: tc.password,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.ExecuteFetch("select route", vtmysql.FETCH_ALL_ROWS, true)
		client.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Rows) != 1 || result.Rows[0][0].ToString() != tc.marker {
			t.Fatalf("user %q routed result = %+v, want %q", tc.username, result, tc.marker)
		}
	}
	if got := testutil.ToFloat64(routeMetric); got != routeMetricBefore+1 {
		t.Fatalf("mysql-a selected route metric = %v, want %v", got, routeMetricBefore+1)
	}

	sticky, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddress.Port, Uname: "alice", Pass: "alice-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	server.UpdateRouteResolver(testRouteResolver{
		"alice": {Backend: targetB},
		"bob":   {Backend: targetB},
	})
	assertMarker := func(client *vtmysql.Conn, want string) {
		t.Helper()
		result, queryErr := client.ExecuteFetch("select route", vtmysql.FETCH_ALL_ROWS, true)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if len(result.Rows) != 1 || result.Rows[0][0].ToString() != want {
			t.Fatalf("routed result = %+v, want %q", result, want)
		}
	}
	assertMarker(sticky, "a")
	sticky.Close()
	reloaded, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddress.Port, Uname: "alice", Pass: "alice-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMarker(reloaded, "b")
	reloaded.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			client, connectErr := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
				Host: "127.0.0.1", Port: proxyAddress.Port,
				Uname: "alice", Pass: "alice-password",
			})
			if connectErr != nil {
				errs <- connectErr
				return
			}
			defer client.Close()
			result, queryErr := client.ExecuteFetch("select route", vtmysql.FETCH_ALL_ROWS, true)
			if queryErr != nil {
				errs <- queryErr
				return
			}
			if len(result.Rows) != 1 || result.Rows[0][0].ToString() != "b" {
				errs <- fmt.Errorf("concurrent routed result = %+v, want b", result)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("routed protocol server did not stop")
	}
}

func TestProtocolServerRequiredInBandTLS(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &testOriginHandler{env: vtenv.NewTestEnv()}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	keyPEM, certPEM, err := tlstest.GetTestKeyAndCert(false)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password"},
		DownstreamUsers:        map[string]string{"client": "client-password"},
		InboundTLS:             &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		RequireSecureTransport: true, ConnectTimeout: time.Second, QueryTimeout: time.Second,
		BackendName: "mysql-tls-test", ProxyOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()

	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	if plaintext, connectErr := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
	}); connectErr == nil {
		plaintext.Close()
		t.Fatal("required-TLS listener accepted a plaintext client")
	}
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
		DbName: "analytics", SslMode: vttls.Required,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	result, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0].ToString() != "42" {
		t.Fatalf("TLS query result = %+v, err = %v", result, err)
	}
	client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolServerCleansUpClientHalfCloses(t *testing.T) {
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewProtocolServer(ProtocolConfig{
		BackendName:     "mysql-disconnect-test",
		DownstreamUsers: map[string]string{"client": "client-password"},
		ProxyOnly:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	address := proxyListener.Addr().(*net.TCPAddr)

	t.Run("handshake", func(t *testing.T) {
		conn, dialErr := net.DialTCP("tcp", nil, address)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		header := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, header); readErr != nil {
			t.Fatal(readErr)
		}
		length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		if _, readErr := io.ReadFull(conn, make([]byte, length)); readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr := conn.CloseWrite(); closeErr != nil {
			t.Fatal(closeErr)
		}
		waitForNoProtocolSessions(t, server.handler)
		conn.Close()
	})

	t.Run("partial query upload", func(t *testing.T) {
		client, connectErr := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
			Host: "127.0.0.1", Port: address.Port, Uname: "client", Pass: "client-password",
		})
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		raw, ok := client.GetRawConn().(*net.TCPConn)
		if !ok {
			t.Fatalf("raw connection type = %T", client.GetRawConn())
		}
		// Claim a 100-byte command packet, provide only a truncated COM_QUERY,
		// then half-close the upload side.
		if _, writeErr := raw.Write([]byte{100, 0, 0, 0, vtmysql.ComQuery, 'S', 'E'}); writeErr != nil {
			t.Fatal(writeErr)
		}
		if closeErr := raw.CloseWrite(); closeErr != nil {
			t.Fatal(closeErr)
		}
		waitForNoProtocolSessions(t, server.handler)
		client.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func waitForNoProtocolSessions(t *testing.T, handler *protocolHandler) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		handler.mtx.Lock()
		count := len(handler.sessions)
		handler.mtx.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("protocol session was not released")
}

func TestOriginQueryTimeoutDiscardsUpstream(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	originHandler := &blockingOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}, release: release,
	}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		BackendName: "mysql-timeout-test", ProxyOnly: true,
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password"},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, QueryTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("timed query error = %v, elapsed = %s", err, time.Since(started))
	}
	close(release)
	if active := server.handler.activeUpstreams.Load(); active != 0 {
		t.Fatalf("active upstreams after timeout = %d", active)
	}
	client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestReconnectReplaysTrackedDatabaseAndTimeZone(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &recordingOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()},
	}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		BackendName: "mysql-replay-test", ProxyOnly: true,
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password", DbName: "analytics"},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.ExecuteFetch("USE reporting", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecuteFetch("SET time_zone = '+00:00'", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}

	server.handler.mtx.Lock()
	var session *upstreamSession
	for _, candidate := range server.handler.sessions {
		session = candidate
		break
	}
	server.handler.mtx.Unlock()
	if session == nil {
		t.Fatal("proxy session was not registered")
	}
	session.mtx.Lock()
	oldUpstream := session.conn
	session.mtx.Unlock()
	if oldUpstream == nil {
		t.Fatal("proxy session had no upstream to discard")
	}
	queryOffset := originHandler.queryLen()
	server.handler.discardUpstream(session, oldUpstream)
	if _, err = client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err != nil {
		t.Fatal(err)
	}

	queries := originHandler.queriesFrom(queryOffset)
	if len(queries) < 3 || !strings.EqualFold(queries[0], "use `reporting`") ||
		!strings.EqualFold(queries[1], "SET time_zone = '+00:00'") ||
		!strings.EqualFold(queries[2], "select 42") {
		t.Fatalf("reconnect query sequence = %q, want database and time-zone replay before query",
			queries)
	}

	client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestLiveQueriesPreserveAndHonorOriginStatusFlags(t *testing.T) {
	const (
		originFlags    = vtmysql.ServerStatusAutocommit | vtmysql.ServerStatusNoBackslashEscapes
		originWarnings = 7
	)
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &protocolStateOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()},
		statusFlags:       originFlags,
		warnings:          originWarnings,
	}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		BackendName: "mysql-protocol-state", ProxyOnly: true,
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password"},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SET SESSION sql_mode = 'NO_BACKSLASH_ESCAPES'",
		"SELECT value FROM events",
	} {
		result, warnings, queryErr := client.ExecuteFetchWithWarningCount(query,
			vtmysql.FETCH_ALL_ROWS, true)
		if queryErr != nil {
			t.Fatalf("%s: %v", query, queryErr)
		}
		if result.StatusFlags != originFlags || warnings != originWarnings {
			t.Fatalf("%s protocol state = flags %#x, warnings %d; want %#x, %d",
				query, result.StatusFlags, warnings, originFlags, originWarnings)
		}
	}
	originQueries := originHandler.queryCount.Load()
	_, queryErr := client.ExecuteFetch(`SELECT 'x\' /*!80400 INTO @answer */`,
		vtmysql.FETCH_ALL_ROWS, true)
	if queryErr == nil || !strings.Contains(queryErr.Error(), "versioned executable comments") {
		t.Fatalf("mode-sensitive query error = %v, want executable-comment rejection", queryErr)
	}
	if got := originHandler.queryCount.Load(); got != originQueries {
		t.Fatalf("origin query count = %d, want %d after local rejection", got, originQueries)
	}
	client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientDisconnectDuringOriginExecutionReleasesSession(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	originHandler := &blockingOriginHandler{
		testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()},
		release:           release, started: started,
	}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		BackendName: "mysql-client-disconnect", ProxyOnly: true,
		Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password"},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second, QueryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()
	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	queryDone := make(chan error, 1)
	go func() {
		_, queryErr := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true)
		queryDone <- queryErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("origin query did not start")
	}
	client.GetRawConn().Close()
	close(release)
	select {
	case <-queryDone:
	case <-time.After(time.Second):
		t.Fatal("client query did not return after disconnect")
	}
	waitForNoProtocolSessions(t, server.handler)
	if active := server.handler.activeUpstreams.Load(); active != 0 {
		t.Fatalf("active upstreams after client disconnect = %d", active)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestOriginDisconnectConnectionState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial bool
	}{
		{name: "before fields"},
		{name: "after partial rows", partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backendName := "mysql-origin-disconnect-" + strings.ReplaceAll(tc.name, " ", "-")
			originListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			originHandler := &disconnectOriginHandler{
				testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}, partial: tc.partial,
			}
			origin, err := vtmysql.NewFromListener(originListener,
				newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
				0, 0, false, false, 0, 0, false)
			if err != nil {
				t.Fatal(err)
			}
			go origin.Accept()
			defer origin.Shutdown()

			proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			originAddr := originListener.Addr().(*net.TCPAddr)
			server, err := NewProtocolServer(ProtocolConfig{
				BackendName: backendName, ProxyOnly: true,
				Upstream: vtmysql.ConnParams{Host: "127.0.0.1", Port: originAddr.Port,
					Uname: "origin", Pass: "origin-password"},
				DownstreamUsers: map[string]string{"client": "client-password"},
				ConnectTimeout:  time.Second, QueryTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(proxyListener) }()
			proxyAddr := proxyListener.Addr().(*net.TCPAddr)
			client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
				Host: "127.0.0.1", Port: proxyAddr.Port, Uname: "client", Pass: "client-password",
			})
			if err != nil {
				t.Fatal(err)
			}
			proxyError := metrics.SQLQueryCache.WithLabelValues(backendName, mysqlDialect,
				sqlanalyzer.CacheModeObject.String(), status.LookupStatusProxyError.String())
			proxyErrorBefore := testutil.ToFloat64(proxyError)
			if _, err := client.ExecuteFetch("select 42", vtmysql.FETCH_ALL_ROWS, true); err == nil {
				t.Fatal("origin disconnect unexpectedly returned a complete result")
			}
			if got := testutil.ToFloat64(proxyError); got != proxyErrorBefore+1 {
				t.Fatalf("proxy-error cache outcomes = %v, want %v", got, proxyErrorBefore+1)
			}
			if tc.partial {
				waitForNoProtocolSessions(t, server.handler)
				if pingErr := client.Ping(); pingErr == nil {
					t.Fatal("partially streamed result left downstream connection reusable")
				}
			} else if pingErr := client.Ping(); pingErr != nil {
				t.Fatalf("pre-field origin disconnect closed reusable downstream: %v", pingErr)
			}
			client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-serveDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProtocolServerDeltaCachesMissingExtent(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &deltaOriginHandler{testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{
			Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password",
		},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second,
		BackendName:     "mysql-delta-test",
		Cache:           newTestCache(),
		CacheTTL:        time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()

	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port,
		Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := func(upper int) string {
		return fmt.Sprintf(`SELECT
  cast(cast(UNIX_TIMESTAMP(ts)/(60) as signed)*60 as signed) AS time,
  count(*) AS value
FROM events
WHERE ts >= FROM_UNIXTIME(0) AND ts < FROM_UNIXTIME(%d)
GROUP BY time
ORDER BY time`, upper)
	}
	first, err := client.ExecuteFetch(query(120), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.ExecuteFetch(query(180), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.ExecuteFetch(query(120), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 2 || len(second.Rows) != 3 || len(again.Rows) != 2 {
		t.Fatalf("delta row counts = %d, %d, %d", len(first.Rows), len(second.Rows), len(again.Rows))
	}
	if got := originHandler.queryCount.Load(); got != 2 {
		t.Fatalf("delta origin queries = %d, want 2", got)
	}
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("protocol server did not stop")
	}
}

func TestProtocolServerDeltaCachesMovingUnalignedRange(t *testing.T) {
	originListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originHandler := &deltaOriginHandler{testOriginHandler: testOriginHandler{env: vtenv.NewTestEnv()}}
	origin, err := vtmysql.NewFromListener(originListener,
		newCredentialAuth(map[string]string{"origin": "origin-password"}, "", nil), originHandler,
		0, 0, false, false, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	go origin.Accept()
	defer origin.Shutdown()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	originAddr := originListener.Addr().(*net.TCPAddr)
	server, err := NewProtocolServer(ProtocolConfig{
		Upstream: vtmysql.ConnParams{
			Host: "127.0.0.1", Port: originAddr.Port,
			Uname: "origin", Pass: "origin-password",
		},
		DownstreamUsers: map[string]string{"client": "client-password"},
		ConnectTimeout:  time.Second,
		BackendName:     "mysql-moving-delta-test",
		Cache:           newTestCache(),
		CacheTTL:        time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(proxyListener) }()

	proxyAddr := proxyListener.Addr().(*net.TCPAddr)
	client, err := vtmysql.Connect(context.Background(), &vtmysql.ConnParams{
		Host: "127.0.0.1", Port: proxyAddr.Port,
		Uname: "client", Pass: "client-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	query := func(lower, upper int) string {
		return fmt.Sprintf(`SELECT UNIX_TIMESTAMP(ts) DIV 60 * 60 AS time,
count(*) AS value FROM events WHERE ts >= FROM_UNIXTIME(%d)
AND ts < FROM_UNIXTIME(%d) GROUP BY time ORDER BY time`, lower, upper)
	}
	first, err := client.ExecuteFetch(query(5, 185), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.ExecuteFetch(query(10, 190), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 2 || len(second.Rows) != 2 {
		t.Fatalf("moving delta row counts = %d, %d", len(first.Rows), len(second.Rows))
	}
	// Both requests normalize to [60, 180), so the second one is a full hit.
	if got := originHandler.queryCount.Load(); got != 1 {
		t.Fatalf("moving delta origin queries = %d, want 1", got)
	}
	emptyFirst, err := client.ExecuteFetch(query(5, 25), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	emptySecond, err := client.ExecuteFetch(query(10, 20), vtmysql.FETCH_ALL_ROWS, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyFirst.Rows) != 0 || len(emptySecond.Rows) != 0 {
		t.Fatalf("normalized empty row counts = %d, %d", len(emptyFirst.Rows), len(emptySecond.Rows))
	}
	if got := originHandler.queryCount.Load(); got != 2 {
		t.Fatalf("normalized empty origin queries = %d, want 2", got)
	}
	client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("protocol server did not stop")
	}
}

type testOriginHandler struct {
	vtmysql.UnimplementedHandler
	env        *vtenv.Environment
	queryCount atomic.Int64
}

type recordingOriginHandler struct {
	testOriginHandler
	mtx     sync.Mutex
	queries []string
}

type protocolStateOriginHandler struct {
	testOriginHandler
	statusFlags uint16
	warnings    uint16
}

func (h *protocolStateOriginHandler) NewConnection(c *vtmysql.Conn) {
	c.StatusFlags = h.statusFlags
}

func (h *protocolStateOriginHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	c.StatusFlags = h.statusFlags
	if isWarningCountQuery(query) {
		return callback(warningCountResult(h.warnings))
	}
	h.queryCount.Add(1)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		return callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_INT64}},
			Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
		})
	}
	return callback(&sqltypes.Result{RowsAffected: 3})
}

func (h *protocolStateOriginHandler) WarningCount(*vtmysql.Conn) uint16 {
	return h.warnings
}

func (h *recordingOriginHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	h.mtx.Lock()
	h.queries = append(h.queries, query)
	h.mtx.Unlock()
	return h.testOriginHandler.ComQuery(c, query, callback)
}

func (h *recordingOriginHandler) queryLen() int {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return len(h.queries)
}

func (h *recordingOriginHandler) queriesFrom(index int) []string {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return append([]string(nil), h.queries[index:]...)
}

type routeOriginHandler struct {
	*testOriginHandler
	marker string
}

func (h *routeOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	return callback(&sqltypes.Result{
		Fields: []*querypb.Field{{Name: "route", Type: querypb.Type_VARCHAR}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewVarChar(h.marker)}},
	})
}

type deltaOriginHandler struct {
	testOriginHandler
}

type blockingOriginHandler struct {
	testOriginHandler
	release     <-chan struct{}
	started     chan struct{}
	startedOnce sync.Once
}

type disconnectOriginHandler struct {
	testOriginHandler
	partial bool
}

func (h *disconnectOriginHandler) ComQuery(c *vtmysql.Conn, _ string,
	callback func(*sqltypes.Result) error,
) error {
	if h.partial {
		value := strings.Repeat("x", 4096)
		rows := make([][]sqltypes.Value, resultBatchSize*4)
		for i := range rows {
			rows[i] = []sqltypes.Value{sqltypes.NewVarBinary(value)}
		}
		if err := callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "value", Type: querypb.Type_VARBINARY}}, Rows: rows,
		}); err != nil {
			return err
		}
	}
	c.Close()
	return io.EOF
}

func (h *blockingOriginHandler) ComQuery(c *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if h.started != nil {
		h.startedOnce.Do(func() { close(h.started) })
	}
	<-h.release
	return h.testOriginHandler.ComQuery(c, query, callback)
}

func (h *deltaOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	h.queryCount.Add(1)
	analysis := defaultAnalyzer.Analyze(query, time.Time{})
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		return fmt.Errorf("unexpected delta origin query: %s", query)
	}
	start := sqlanalyzer.FloorBucket(analysis.Plan.LowerBound.Value, analysis.Plan.Step, 0)
	end := sqlanalyzer.FloorBucket(analysis.Plan.UpperBound.Value.Add(-time.Nanosecond), analysis.Plan.Step, 0)
	rows := make([][]sqltypes.Value, 0, int(end.Sub(start)/analysis.Plan.Step)+1)
	for current := start; !current.After(end); current = current.Add(analysis.Plan.Step) {
		rows = append(rows, []sqltypes.Value{
			sqltypes.NewInt64(current.Unix()), sqltypes.NewInt64(current.Unix()/60 + 1),
		})
	}
	return callback(&sqltypes.Result{
		Fields: []*querypb.Field{
			{Name: "time", Type: querypb.Type_INT64},
			{Name: "value", Type: querypb.Type_INT64},
		},
		Rows: rows,
	})
}

func (h *testOriginHandler) Env() *vtenv.Environment { return h.env }

func (h *testOriginHandler) ComQuery(_ *vtmysql.Conn, query string,
	callback func(*sqltypes.Result) error,
) error {
	if isWarningCountQuery(query) {
		return callback(warningCountResult(0))
	}
	h.queryCount.Add(1)
	if strings.EqualFold(strings.TrimSpace(query), "select 42") {
		return callback(&sqltypes.Result{
			Fields: []*querypb.Field{{Name: "answer", Type: querypb.Type_INT64}},
			Rows:   [][]sqltypes.Value{{sqltypes.NewInt64(42)}},
		})
	}
	return callback(&sqltypes.Result{})
}

func isWarningCountQuery(query string) bool {
	return strings.EqualFold(strings.TrimSpace(query), warningCountQuery)
}

func warningCountResult(count uint16) *sqltypes.Result {
	return &sqltypes.Result{
		Fields: []*querypb.Field{{Name: "@@session.warning_count", Type: querypb.Type_UINT64}},
		Rows:   [][]sqltypes.Value{{sqltypes.NewUint64(uint64(count))}},
	}
}

func (h *testOriginHandler) ComQueryMulti(_ *vtmysql.Conn, _ string,
	_ func(sqltypes.QueryResponse, bool, bool) error,
) error {
	return unsupported("multi-statements")
}

func (h *testOriginHandler) ComPrepare(_ *vtmysql.Conn, _ string) ([]*querypb.Field, uint16, error) {
	return nil, 0, unsupported("prepared statements")
}

func (h *testOriginHandler) ComStmtExecute(_ *vtmysql.Conn, _ *vtmysql.PrepareData,
	_ func(*sqltypes.Result) error,
) error {
	return unsupported("prepared statements")
}

func (h *testOriginHandler) ComRegisterReplica(_ *vtmysql.Conn, _ string, _ uint16, _, _ string) error {
	return unsupported("replica registration")
}

func (h *testOriginHandler) ComBinlogDump(_ *vtmysql.Conn, _ string, _ uint32) error {
	return unsupported("binlog streaming")
}

func (h *testOriginHandler) ComBinlogDumpGTID(_ *vtmysql.Conn, _ string, _ uint64,
	_ replication.GTIDSet, _ uint16,
) error {
	return unsupported("GTID binlog streaming")
}

func (h *testOriginHandler) WarningCount(*vtmysql.Conn) uint16 { return 0 }

type testCache struct {
	mtx           sync.Mutex
	data          map[string][]byte
	storeErr      error
	retrieveErr   error
	removeErr     error
	configuration *cacheoptions.Options
}

func newTestCache() *testCache { return &testCache{data: make(map[string][]byte)} }

func (c *testCache) Connect() error { return nil }

func (c *testCache) Store(key string, data []byte, _ time.Duration) error {
	if c.storeErr != nil {
		return c.storeErr
	}
	c.mtx.Lock()
	c.data[key] = append([]byte(nil), data...)
	c.mtx.Unlock()
	return nil
}

func (c *testCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	if c.retrieveErr != nil {
		return nil, status.LookupStatusError, c.retrieveErr
	}
	c.mtx.Lock()
	data, ok := c.data[key]
	c.mtx.Unlock()
	if !ok {
		return nil, status.LookupStatusKeyMiss, trickstercache.ErrKNF
	}
	return append([]byte(nil), data...), status.LookupStatusHit, nil
}

func (c *testCache) Remove(keys ...string) error {
	if c.removeErr != nil {
		return c.removeErr
	}
	c.mtx.Lock()
	for _, key := range keys {
		delete(c.data, key)
	}
	c.mtx.Unlock()
	return nil
}

func (c *testCache) Close() error { return nil }

func (c *testCache) Configuration() *cacheoptions.Options {
	if c.configuration != nil {
		return c.configuration
	}
	return cacheoptions.New()
}
