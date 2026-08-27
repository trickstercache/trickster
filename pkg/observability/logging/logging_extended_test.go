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

package logging

import (
	"bytes"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
)

type stringerVal struct{ s string }

func (s stringerVal) String() string { return s.s }

func TestNoopLogger(t *testing.T) {
	t.Parallel()

	l := NoopLogger()
	l.Info("ignored", Pairs{"k": "v"})
	if l.Level() != level.Info {
		t.Fatalf("Level() = %s, want info", l.Level())
	}
}

func TestLoggerWriteAndFiltering(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Warn)
	l.SetLogAsynchronous(false)

	l.Info("hidden", nil)
	l.Warn("visible", Pairs{"key": "value"})
	if !strings.Contains(buf.String(), "visible") {
		t.Fatalf("output = %q", buf.String())
	}
	if strings.Contains(buf.String(), "hidden") {
		t.Fatal("info message should be filtered at warn level")
	}

	n, err := l.(*logger).Write([]byte("direct write\n"))
	if err != nil || n == 0 {
		t.Fatalf("Write() = (%d, %v)", n, err)
	}
	if !strings.Contains(buf.String(), "direct write") {
		t.Fatal("expected direct write output")
	}
}

func TestLoggerOnceAndSynchronousHelpers(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Debug)
	l.SetLogAsynchronous(false)

	if !l.DebugOnce("once-key", "debug once", nil) {
		t.Fatal("expected first DebugOnce to log")
	}
	if l.DebugOnce("once-key", "debug once", nil) {
		t.Fatal("expected second DebugOnce to be suppressed")
	}
	if !l.HasDebuggedOnce("once-key") {
		t.Fatal("expected HasDebuggedOnce true")
	}

	l.LogSynchronous(level.Error, "sync error", Pairs{
		"err":   errors.New("boom"),
		"label": stringerVal{s: "has space"},
		"plain": "x",
	})
	out := buf.String()
	if !strings.Contains(out, "sync error") || !strings.Contains(out, "boom") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, `"has space"`) {
		t.Fatalf("expected quoted stringer value in %q", out)
	}
}

func TestLoggerAsyncMode(t *testing.T) {
	t.Parallel()

	buf := &syncBuffer{}
	l := StreamLogger(buf, level.Info)
	l.SetLogAsynchronous(true)
	l.Info("async entry", nil)
	deadline := time.Now().Add(500 * time.Millisecond)
	for buf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if buf.Len() == 0 {
		t.Fatal("expected async log output")
	}
}

type syncBuffer struct {
	mtx sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	return b.buf.Len()
}

func TestQuoteAsNeeded(t *testing.T) {
	t.Parallel()

	if quoteAsNeeded("plain") != "plain" {
		t.Fatal("expected unquoted plain value")
	}
	if quoteAsNeeded(`has space`) != `"has space"` {
		t.Fatalf("quoteAsNeeded = %q", quoteAsNeeded(`has space`))
	}
}

func TestGetCallerSkipsLoggingFrames(t *testing.T) {
	t.Parallel()

	if got := getCaller(0); got != "" {
		t.Fatalf("caller = %q, want empty when invoked from logging tests", got)
	}
}

func TestLoggerCloseWithoutCloser(t *testing.T) {
	t.Parallel()

	l := ConsoleLogger(level.Info)
	l.Close()
}

func TestLogWithTrimmedEvent(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Info)
	l.SetLogAsynchronous(false)
	l.Info("  spaced event  ", Pairs{"k": fmt.Sprintf("%v", 1)})
	if !strings.Contains(buf.String(), `event="spaced event"`) {
		t.Fatalf("output = %q", buf.String())
	}
}

type closableBuffer struct {
	bytes.Buffer
	closed bool
}

func (c *closableBuffer) Close() error {
	c.closed = true
	return nil
}

func TestStreamLoggerWithCloser(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	sl := StreamLogger(w, level.Error)
	sl.SetLogAsynchronous(false)
	sl.ErrorSynchronous("sync", nil)
	if w.Body.Len() == 0 {
		t.Fatal("expected synchronous error output")
	}
	sl.Close()
}

func TestStreamLoggerSetsCloser(t *testing.T) {
	t.Parallel()

	buf := &closableBuffer{}
	l := StreamLogger(buf, level.Info)
	l.SetLogAsynchronous(false)
	l.Info("closer test", nil)
	l.Close()
	if !buf.closed {
		t.Fatal("expected StreamLogger to close underlying closer")
	}
	if !strings.Contains(buf.String(), "closer test") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestLogAndSynchronousLevelFiltering(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Error)
	l.SetLogAsynchronous(false)

	l.Log(level.Info, "filtered info", nil)
	l.Log(level.Level("bogus"), "invalid level", nil)
	l.LogSynchronous(level.Warn, "filtered sync warn", nil)
	l.LogSynchronous(level.Level("bogus"), "invalid sync", nil)
	l.DebugSynchronous("filtered debug sync", nil)
	l.InfoSynchronous("filtered info sync", nil)
	l.WarnSynchronous("filtered warn sync", nil)

	if buf.Len() != 0 {
		t.Fatalf("expected filtered messages, got %q", buf.String())
	}

	l.Log(level.Error, "allowed error", nil)
	l.LogSynchronous(level.Error, "allowed sync error", nil)
	l.DebugSynchronous("still filtered", nil)
	out := buf.String()
	if !strings.Contains(out, "allowed error") || !strings.Contains(out, "allowed sync error") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "still filtered") {
		t.Fatal("debug should remain filtered at error level")
	}
}

func TestSynchronousHelpersAtDebug(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Debug)
	l.SetLogAsynchronous(false)

	l.DebugSynchronous("sync debug", Pairs{"a": 1})
	l.InfoSynchronous("sync info", nil)
	l.WarnSynchronous("sync warn", nil)

	out := buf.String()
	for _, want := range []string{"sync debug", "sync info", "sync warn"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestLogOnceAndHasHelpers(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := StreamLogger(buf, level.Debug)
	l.SetLogAsynchronous(false)

	if l.LogOnce(level.Level("bogus"), "bad", "nope", nil) {
		t.Fatal("invalid level should not log once")
	}
	if l.LogOnce(level.Debug, "k1", "once debug", nil) != true {
		t.Fatal("expected first LogOnce to succeed")
	}
	if l.LogOnce(level.Debug, "k1", "once debug", nil) {
		t.Fatal("expected second LogOnce to be suppressed")
	}
	if !l.HasLoggedOnce(level.Debug, "k1") {
		t.Fatal("expected HasLoggedOnce true")
	}

	if !l.InfoOnce("info-key", "once info", nil) {
		t.Fatal("expected InfoOnce true")
	}
	if !l.HasInfoedOnce("info-key") {
		t.Fatal("expected HasInfoedOnce true")
	}

	if !l.ErrorOnce("error-key", "once error", nil) {
		t.Fatal("expected ErrorOnce true")
	}
	if !l.HasErroredOnce("error-key") {
		t.Fatal("expected HasErroredOnce true")
	}

	// Below configured level should not log-once.
	l.SetLogLevel(level.Error)
	if l.LogOnce(level.Info, "below", "hidden", nil) {
		t.Fatal("below-level LogOnce should return false")
	}
}

func TestWriteNilWriterAndNilLogWriter(t *testing.T) {
	t.Parallel()

	l := &logger{now: time.Now}
	n, err := l.Write([]byte("x"))
	if err != nil || n != 0 {
		t.Fatalf("Write(nil writer) = (%d, %v)", n, err)
	}
	l.logWithCaller(level.Info, "noop", nil, "")
}

func TestItemBytes(t *testing.T) {
	t.Parallel()

	got := string((&item{key: "k", val: "v"}).Bytes())
	if got != "k=v" {
		t.Fatalf("Bytes() = %q, want k=v", got)
	}
}

func TestLogWithCallerField(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	l := &logger{
		writer:  buf,
		level:   level.Info,
		levelID: level.InfoID,
		now:     func() time.Time { return time.Time{} },
	}
	l.logWithCaller(level.Info, "with-caller", nil, "pkg/example/file.go:10")
	out := buf.String()
	if !strings.Contains(out, "caller=pkg/example/file.go:10") {
		t.Fatalf("output = %q", out)
	}
}

func TestFatalExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		wantCode int
	}{
		{name: "nonzero", code: 2, wantCode: 2},
		{name: "zero_becomes_one", code: 0, wantCode: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if os.Getenv("LOGGING_FATAL_TEST") == tc.name {
				l := NoopLogger()
				l.Fatal(tc.code, "fatal exit", nil)
				return
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestFatalExitCodes/"+tc.name+"$", "-test.v")
			cmd.Env = append(os.Environ(), "LOGGING_FATAL_TEST="+tc.name)
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("expected ExitError, got %v", err)
			}
			if ee.ExitCode() != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", ee.ExitCode(), tc.wantCode)
			}
		})
	}
}
