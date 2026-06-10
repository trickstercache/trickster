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
	"strings"
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

	buf := &bytes.Buffer{}
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
