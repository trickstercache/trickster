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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"

	"github.com/stretchr/testify/require"
)

func TestSetLoggerNilIsNoop(t *testing.T) {
	prev := Logger()
	t.Cleanup(func() { SetLogger(prev) })

	SetLogger(nil)
	require.Equal(t, prev, Logger())
}

func TestPackageLoggerWrappers(t *testing.T) {
	buf := &bytes.Buffer{}
	l := logging.StreamLogger(buf, level.Debug)
	l.SetLogAsynchronous(false)
	SetLogger(l)
	t.Cleanup(func() {
		SetLogger(logging.NoopLogger())
	})

	require.Equal(t, l, Logger())
	require.Equal(t, level.Debug, Level())

	SetLogLevel(level.Info)
	require.Equal(t, level.Info, Level())
	SetLogLevel(level.Debug)

	SetLogAsynchronous(false)

	Log(level.Info, "log event", logging.Pairs{"k": "v"})
	Debug("debug event", logging.Pairs{"k": "v"})
	Info("info event", logging.Pairs{"k": "v"})
	Warn("warn event", logging.Pairs{"k": "v"})
	Error("error event", logging.Pairs{"k": "v"})
	Fatal(-1, "fatal event", logging.Pairs{"k": "v"})

	LogSynchronous(level.Warn, "sync log", nil)
	DebugSynchronous("sync debug", nil)
	InfoSynchronous("sync info", nil)
	WarnSynchronous("sync warn", nil)
	ErrorSynchronous("sync error", nil)

	require.True(t, LogOnce(level.Info, "once-log", "once log", nil))
	require.False(t, LogOnce(level.Info, "once-log", "once log", nil))
	require.True(t, HasLoggedOnce(level.Info, "once-log"))

	require.True(t, DebugOnce("once-debug", "once debug", nil))
	require.True(t, HasDebuggedOnce("once-debug"))
	require.False(t, DebugOnce("once-debug", "once debug", nil))

	require.True(t, InfoOnce("once-info", "once info", nil))
	require.True(t, HasInfoedOnce("once-info"))
	require.False(t, InfoOnce("once-info", "once info", nil))

	require.True(t, WarnOnce("once-warn", "once warn", nil))
	require.True(t, HasWarnedOnce("once-warn"))
	require.False(t, WarnOnce("once-warn", "once warn", nil))

	require.True(t, ErrorOnce("once-error", "once error", nil))
	require.True(t, HasErroredOnce("once-error"))
	require.False(t, ErrorOnce("once-error", "once error", nil))

	out := buf.String()
	for _, want := range []string{
		"log event", "debug event", "info event", "warn event", "error event", "fatal event",
		"sync log", "sync debug", "sync info", "sync warn", "sync error",
		"once log", "once debug", "once info", "once warn", "once error",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output: %q", want, out)
		}
	}
}

func TestPackageLoggerAsync(t *testing.T) {
	buf := &syncBuffer{}
	l := logging.StreamLogger(buf, level.Info)
	SetLogger(l)
	t.Cleanup(func() {
		SetLogger(logging.NoopLogger())
	})

	SetLogAsynchronous(true)
	Info("async package log", nil)

	deadline := time.Now().Add(500 * time.Millisecond)
	for buf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Contains(t, buf.String(), "async package log")
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

func (b *syncBuffer) String() string {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	return b.buf.String()
}
