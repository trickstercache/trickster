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
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	fakeAuthTrust     = "trust"
	fakeAuthCleartext = "cleartext"
	fakeAuthMD5       = "md5"
	fakeAuthSCRAM     = "scram"

	// queries the fake origin understands
	fakeQueryOne           = "select 1"
	fakeQuerySlow          = "select pg_sleep(60)"
	fakeQueryError         = "select 1/0"
	fakeQueryMany          = "select generate_series(1, 5000)"
	fakeManyRows           = 5000
	fakeQueryBegin         = "BEGIN"
	fakeBucketFunction     = "date_bin"
	fakeHostColumn         = "host"
	fakeBucketStep         = 5 * time.Minute
	fakeZoneUTC            = "UTC"
	fakeDateStyleISO       = "ISO, MDY"
	fakeParamDateStyle     = "DateStyle"
	fakeNoticeText         = "a notice from the origin"
	sqlstateSyntaxError    = "42601"
	fakeQueryCommit        = "COMMIT"
	fakeQuerySetZone       = "SET TIME ZONE "
	fakeQueryPing          = "-- ping"
	fakeQueryWide          = "select wide"
	fakeDefaultFloatDigits = "1"
	fakeTextOID            = 25
	fakeWideRowBytes       = 8 << 20
	fakeWideRows           = 4

	fakeServerVersion  = "18.6"
	fakeLongSecretLen  = 32
	fakeTimeout        = 5 * time.Second
	sqlstateCanceled   = "57014"
	sqlstateDivByZero  = "22012"
	fakeParamTimeZone  = "TimeZone"
	fakeParamVersion   = "server_version"
	fakeColumnName     = "value"
	fakeTestCertName   = "localhost"
	fakeLoopbackListen = "127.0.0.1:0"
)

var fakeBoundPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)

type fakeCancel struct {
	pid    uint32
	secret []byte
}

type fakeUpstream struct {
	t          *testing.T
	listener   net.Listener
	tls        *tls.Config
	authMode   string
	user       string
	password   string
	longSecret bool
	reject     func(sql string) bool
	notice     bool
	dateStyle  string
	timeOID    uint32
	// floatDigits is the session's extra_float_digits, as a role default would set it
	floatDigits string

	mtx      sync.Mutex
	nextPID  uint32
	startups []map[string]string
	queries  []string
	running  map[uint32]chan struct{}
	cancels  chan fakeCancel
	wg       sync.WaitGroup
}

func newFakeUpstream(t *testing.T, mutate func(*fakeUpstream)) *fakeUpstream {
	t.Helper()
	l, err := net.Listen("tcp", fakeLoopbackListen)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUpstream{
		t: t, listener: l, authMode: fakeAuthTrust, running: make(map[uint32]chan struct{}),
		cancels: make(chan fakeCancel, 16),
	}
	if mutate != nil {
		mutate(f)
	}
	f.wg.Add(1)
	go f.accept()
	t.Cleanup(func() {
		_ = l.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeUpstream) address() string { return f.listener.Addr().String() }

func (f *fakeUpstream) lastStartup() map[string]string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if len(f.startups) == 0 {
		return nil
	}
	return f.startups[len(f.startups)-1]
}

func (f *fakeUpstream) accept() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.wg.Go(func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
			f.serve(conn)
		})
	}
}

func (f *fakeUpstream) serve(conn net.Conn) {
	backend := pgproto3.NewBackend(conn, conn)
	for {
		message, err := backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
		switch m := message.(type) {
		case *pgproto3.SSLRequest:
			if f.tls == nil {
				_, _ = conn.Write([]byte{sslRefused})
				continue
			}
			_, _ = conn.Write([]byte{sslAccepted})
			secured := tls.Server(conn, f.tls)
			if secured.Handshake() != nil {
				return
			}
			conn = secured
			backend = pgproto3.NewBackend(conn, conn)
		case *pgproto3.CancelRequest:
			f.cancel(m.ProcessID, m.SecretKey)
			return
		case *pgproto3.StartupMessage:
			f.session(backend, m)
			return
		default:
			return
		}
	}
}

func (f *fakeUpstream) cancel(pid uint32, secret []byte) {
	f.cancels <- fakeCancel{pid: pid, secret: secret}
	f.mtx.Lock()
	running := f.running[pid]
	delete(f.running, pid)
	f.mtx.Unlock()
	if running != nil {
		close(running)
	}
}

func (f *fakeUpstream) session(backend *pgproto3.Backend, startup *pgproto3.StartupMessage) {
	f.mtx.Lock()
	f.startups = append(f.startups, startup.Parameters)
	f.nextPID++
	pid := f.nextPID + 1000
	f.mtx.Unlock()
	if startup.ProtocolVersion == pgproto3.ProtocolVersion32 && !f.longSecret {
		backend.Send(&pgproto3.NegotiateProtocolVersion{NewestMinorProtocol: pgproto3.ProtocolVersion30})
	}
	if !f.authenticate(backend, startup.Parameters[paramUser]) {
		backend.Send(&pgproto3.ErrorResponse{Severity: severityFatal, Code: sqlstateInvalidPassword,
			Message: "password authentication failed"})
		_ = backend.Flush()
		return
	}
	secret := []byte{1, 2, 3, 4}
	if f.longSecret {
		secret = make([]byte, fakeLongSecretLen)
		for i := range secret {
			secret[i] = byte(i + 1)
		}
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: fakeParamVersion, Value: fakeServerVersion})
	zone := startup.Parameters[fakeParamTimeZone]
	if zone == "" {
		zone = fakeZoneUTC
	}
	dateStyle := f.dateStyle
	if dateStyle == "" {
		dateStyle = fakeDateStyleISO
	}
	backend.Send(&pgproto3.ParameterStatus{Name: fakeParamTimeZone, Value: zone})
	backend.Send(&pgproto3.ParameterStatus{Name: fakeParamDateStyle, Value: dateStyle})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: pid, SecretKey: secret})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	txStatus := byte('I')
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch m := message.(type) {
		case *pgproto3.Query:
			txStatus = f.query(backend, pid, m.String, txStatus)
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: txStatus})
		case *pgproto3.Parse:
			backend.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			backend.Send(&pgproto3.BindComplete{})
		case *pgproto3.Describe:
			backend.Send(f.rowDescription())
		case *pgproto3.Execute:
			f.rows(backend, 1)
		case *pgproto3.Sync:
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: txStatus})
		case *pgproto3.Terminate:
			return
		}
		if backend.Flush() != nil {
			return
		}
	}
}

func (f *fakeUpstream) authenticate(backend *pgproto3.Backend, user string) bool {
	switch f.authMode {
	case fakeAuthCleartext:
		backend.Send(&pgproto3.AuthenticationCleartextPassword{})
		_ = backend.SetAuthType(pgproto3.AuthTypeCleartextPassword)
		if backend.Flush() != nil {
			return false
		}
		message, err := backend.Receive()
		password, ok := message.(*pgproto3.PasswordMessage)
		return err == nil && ok && user == f.user && password.Password == f.password
	case fakeAuthMD5:
		salt := [4]byte{9, 8, 7, 6}
		backend.Send(&pgproto3.AuthenticationMD5Password{Salt: salt})
		_ = backend.SetAuthType(pgproto3.AuthTypeMD5Password)
		if backend.Flush() != nil {
			return false
		}
		message, err := backend.Receive()
		password, ok := message.(*pgproto3.PasswordMessage)
		inner := cred.PostgresMD5(f.user, f.password)[len(cred.PostgresMD5Prefix):]
		sum := md5.Sum(append([]byte(inner), salt[:]...))
		return err == nil && ok && password.Password == cred.PostgresMD5Prefix+hex.EncodeToString(sum[:])
	case fakeAuthSCRAM:
		return f.authenticateSCRAM(backend, user)
	}
	return true
}

func (f *fakeUpstream) authenticateSCRAM(backend *pgproto3.Backend, user string) bool {
	verifier, err := cred.NewSCRAMVerifier(f.password, []byte("fake-upstream-salt"), cred.DefaultSCRAMIterations)
	if err != nil {
		return false
	}
	exchange := &scramServer{verifier: verifier, known: user == f.user}
	backend.Send(&pgproto3.AuthenticationSASL{AuthMechanisms: exchange.mechanisms()})
	_ = backend.SetAuthType(pgproto3.AuthTypeSASL)
	if backend.Flush() != nil {
		return false
	}
	message, err := backend.Receive()
	initial, ok := message.(*pgproto3.SASLInitialResponse)
	if err != nil || !ok {
		return false
	}
	serverFirst, err := exchange.first(initial.AuthMechanism, initial.Data)
	if err != nil {
		return false
	}
	backend.Send(&pgproto3.AuthenticationSASLContinue{Data: serverFirst})
	_ = backend.SetAuthType(pgproto3.AuthTypeSASLContinue)
	if backend.Flush() != nil {
		return false
	}
	message, err = backend.Receive()
	response, ok := message.(*pgproto3.SASLResponse)
	if err != nil || !ok {
		return false
	}
	serverFinal, err := exchange.final(response.Data)
	if err != nil {
		return false
	}
	backend.Send(&pgproto3.AuthenticationSASLFinal{Data: serverFinal})
	return true
}

func (f *fakeUpstream) rowDescription() *pgproto3.RowDescription {
	return &pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{
		Name: []byte(fakeColumnName), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1,
	}}}
}

func (f *fakeUpstream) rows(backend *pgproto3.Backend, n int) {
	for i := 1; i <= n; i++ {
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(strconv.Itoa(i))}})
	}
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT " + strconv.Itoa(n))})
}

func (f *fakeUpstream) query(backend *pgproto3.Backend, pid uint32, sql string, txStatus byte) byte {
	f.mtx.Lock()
	f.queries = append(f.queries, sql)
	f.mtx.Unlock()
	switch {
	case f.reject != nil && f.reject(sql):
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: sqlstateSyntaxError, Message: "syntax error"})
		return txStatus
	case strings.Contains(sql, fakeBucketFunction):
		f.buckets(backend, sql)
		return txStatus
	case sql == fakeQueryBegin:
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
		return 'T'
	case sql == fakeQueryCommit:
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
		return 'I'
	case strings.HasPrefix(sql, fakeQuerySetZone):
		// the zone is a reported setting, so the change is announced
		backend.Send(&pgproto3.ParameterStatus{Name: fakeParamTimeZone, Value: strings.Trim(sql[len(fakeQuerySetZone):], "' ")})
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
		return txStatus
	case strings.HasPrefix(sql, "SET ") || strings.HasPrefix(sql, "RESET "):
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
		return txStatus
	}
	switch sql {
	case fakeQuerySlow:
		canceled := make(chan struct{})
		f.mtx.Lock()
		f.running[pid] = canceled
		f.mtx.Unlock()
		select {
		case <-canceled:
			backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: sqlstateCanceled,
				Message: "canceling statement due to user request"})
		case <-time.After(fakeTimeout):
			f.t.Error("slow query was never canceled")
		}
	case fakeQueryError:
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: sqlstateDivByZero, Message: "division by zero"})
	case fakeQueryMany:
		backend.Send(f.rowDescription())
		f.rows(backend, fakeManyRows)
	case unannouncedSettingsSQL:
		digits := f.floatDigits
		if digits == "" {
			digits = fakeDefaultFloatDigits
		}
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{
			{Name: []byte("current_setting"), DataTypeOID: fakeTextOID}, {Name: []byte("current_setting"), DataTypeOID: fakeTextOID},
		}})
		backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(digits), []byte("hex")}})
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
	case fakeQueryWide:
		// two small rows, one far larger than any buffering limit, and one more
		backend.Send(f.rowDescription())
		for _, value := range [][]byte{[]byte("1"), []byte("2"), fakeWideValue(), []byte("4")} {
			backend.Send(&pgproto3.DataRow{Values: [][]byte{value}})
		}
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT " + strconv.Itoa(fakeWideRows))})
	case "", fakeQueryPing:
		backend.Send(&pgproto3.EmptyQueryResponse{})
	default:
		backend.Send(f.rowDescription())
		f.rows(backend, 1)
	}
	return txStatus
}

func (f *fakeUpstream) received() []string {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return slices.Clone(f.queries)
}

func (f *fakeUpstream) forget() {
	f.mtx.Lock()
	f.queries = nil
	f.mtx.Unlock()
}

func fakeBucketValue(bucket time.Time, host string) string {
	// the origin's data is a pure function of the bucket and host
	return strconv.Itoa(bucket.Hour()*100 + bucket.Minute() + len(host)*1000)
}

func (f *fakeUpstream) buckets(backend *pgproto3.Backend, sql string) {
	// one row per five-minute bucket between the statement's two bounds, or
	// two when the statement groups by host
	bounds := fakeBoundPattern.FindAllString(sql, -1)
	if len(bounds) < 2 {
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: sqlstateSyntaxError, Message: "no bounds"})
		return
	}
	lower, _ := time.Parse(time.RFC3339Nano, bounds[0])
	upper, _ := time.Parse(time.RFC3339Nano, bounds[1])
	grouped, descending := strings.Contains(sql, fakeHostColumn), strings.Contains(sql, "DESC")
	timeOID := f.timeOID
	if timeOID == 0 {
		timeOID = OIDTimestampTZ
	}
	fields := []pgproto3.FieldDescription{{Name: []byte("time"), DataTypeOID: timeOID, DataTypeSize: 8, TypeModifier: -1}}
	if grouped {
		fields = append(fields, pgproto3.FieldDescription{Name: []byte(fakeHostColumn), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1})
	}
	fields = append(fields, pgproto3.FieldDescription{Name: []byte(fakeColumnName), DataTypeOID: OIDInt8, DataTypeSize: 8, TypeModifier: -1})
	if f.notice {
		backend.Send(&pgproto3.NoticeResponse{Severity: "NOTICE", Code: "00000", Message: fakeNoticeText})
	}
	backend.Send(&pgproto3.RowDescription{Fields: fields})
	var times []time.Time
	for bucket := lower.Truncate(fakeBucketStep); bucket.Before(upper); bucket = bucket.Add(fakeBucketStep) {
		if !bucket.Before(lower) {
			times = append(times, bucket)
		}
	}
	if descending {
		slices.Reverse(times)
	}
	rows := 0
	for _, bucket := range times {
		text := bucket.UTC().Format("2006-01-02 15:04:05") + "+00"
		hosts := []string{""}
		if grouped {
			// within a bucket the origin orders hosts its own way: longest first
			hosts = []string{"zeta-long", "alpha"}
		}
		for _, host := range hosts {
			values := [][]byte{[]byte(text)}
			if grouped {
				values = append(values, []byte(host))
			}
			backend.Send(&pgproto3.DataRow{Values: append(values, []byte(fakeBucketValue(bucket, host)))})
			rows++
		}
	}
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT " + strconv.Itoa(rows))})
}

func (f *fakeUpstream) isRunning() bool {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return len(f.running) > 0
}

func testServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, cert, err := tlstest.GetTestKeyAndCertWithNames(fakeTestCertName)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}

func fakeWideValue() []byte {
	value := bytes.Repeat([]byte{'x'}, fakeWideRowBytes)
	value[0], value[len(value)-1] = '<', '>'
	return value
}
