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

package logger

import (
	"bytes"
	stdlog "log"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"

	"github.com/stretchr/testify/require"
)

const testHandshakeLine = "http: TLS handshake error from 192.0.2.1:34412: EOF\n"

func useBufferLogger(t *testing.T, logLevel level.Level) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	l := logging.StreamLogger(buf, logLevel)
	l.SetLogAsynchronous(false)
	SetLogger(l)
	t.Cleanup(func() { SetLogger(logging.NoopLogger()) })
	return buf
}

func TestHTTPServerErrorLogHandshakeAtInfo(t *testing.T) {
	buf := useBufferLogger(t, level.Info)
	HTTPServerErrorLog("default").Print(testHandshakeLine)
	require.Empty(t, buf.String())
}

func TestHTTPServerErrorLogHandshakeAtDebug(t *testing.T) {
	buf := useBufferLogger(t, level.Debug)
	HTTPServerErrorLog("default").Print(testHandshakeLine)
	out := buf.String()
	require.Contains(t, out, "level=debug")
	require.Contains(t, out, tlsHandshakeEvent)
	require.Contains(t, out, "listenerName=default")
	require.Contains(t, out, "192.0.2.1:34412")
}

func TestHTTPServerErrorLogOtherAtWarn(t *testing.T) {
	buf := useBufferLogger(t, level.Info)
	HTTPServerErrorLog("default").Print("http: superfluous response.WriteHeader call\n")
	out := buf.String()
	require.Contains(t, out, "level=warn")
	require.Contains(t, out, httpServerErrorEvent)
	require.Contains(t, out, "superfluous")
}

func TestStdlogWriterEmptyLine(t *testing.T) {
	buf := useBufferLogger(t, level.Debug)
	w := &stdlogWriter{event: stdlogEvent}
	n, err := w.Write([]byte("\n"))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, buf.String())
}

func TestCaptureStdLog(t *testing.T) {
	prevOut, prevFlags, prevPrefix := stdlog.Writer(), stdlog.Flags(), stdlog.Prefix()
	t.Cleanup(func() {
		stdlog.SetOutput(prevOut)
		stdlog.SetFlags(prevFlags)
		stdlog.SetPrefix(prevPrefix)
	})
	stdlog.SetPrefix("log: ")
	buf := useBufferLogger(t, level.Info)
	CaptureStdLog()
	stdlog.Print(testHandshakeLine)
	out := buf.String()
	require.Contains(t, out, "level=warn")
	require.Contains(t, out, stdlogEvent)
	require.NotContains(t, out, "log: ")
}
