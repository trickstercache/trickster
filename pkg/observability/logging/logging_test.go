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
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sizeconv"

	"github.com/stretchr/testify/require"
)

func TestConsoleLogger(t *testing.T) {
	testCases := []string{
		"debug",
		"info",
		"warn",
		"error",
	}
	// it should create a logger for each level
	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			ltc := level.Level(tc)
			l := ConsoleLogger(ltc)
			if l.Level() != ltc {
				t.Errorf("mismatch in log level: expected=%s actual=%s", tc, l.Level())
			}
		})
	}
}

func TestNew(t *testing.T) {
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogLevel: "info"}
	log := New(conf)
	if log.Level() != level.Info {
		t.Errorf("expected %s got %s", "info", log.Level())
	}
	if log.(*logger).closer != nil {
		t.Error("console logger must not own stdout")
	}
}

func TestNewConflictFallbackDoesNotOwnStdout(t *testing.T) {
	conf := config.NewConfig()
	conf.Logging.LogFile = t.TempDir() + "/shared.log"
	first := New(conf)
	defer first.Close()

	conflict := conf.Clone()
	count := manager.DefaultRetentionCount - 1
	conflict.Logging.Retention = &manager.RetentionOptions{Count: &count}
	second := New(conflict).(*logger)
	if second.writer != os.Stdout {
		t.Fatal("expected conflicting writer to fall back to stdout")
	}
	if second.closer != nil {
		t.Error("stdout fallback must not be owned by the logger")
	}
	second.Close()
}

func TestNewLogger_LogFile(t *testing.T) {
	td := t.TempDir()
	fileName := td + "/out.log"
	instanceFileName := td + "/out.1.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 1}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "info"}
	log := New(conf)
	l := log.(*logger)
	l.now = func() time.Time {
		return time.Time{}
	}
	log.SetLogAsynchronous(false)
	log.Info("testEntry ", Pairs{
		"testKey":  "test Val",
		"testKey2": "testValue2",
		"testKey3": "testValue3",
	})
	if _, err := os.Stat(instanceFileName); err != nil {
		t.Error(err)
	}
	log.Close()
	// now inspect the file for consistent output
	b, err := os.ReadFile(instanceFileName)
	require.NoError(t, err)
	require.Equal(t, `time=0001-01-01T00:00:00Z app=trickster level=info event=testEntry testKey="test Val" testKey2=testValue2 testKey3=testValue3`+"\n", string(b))
}

func TestNewLogger_RotationOptions(t *testing.T) {
	fileName := t.TempDir() + "/out.log"
	size := sizeconv.Size(16)
	count := 2
	compress := false
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{
		LogFile:   fileName,
		LogLevel:  "info",
		Rotation:  &manager.RotationOptions{Size: &size},
		Retention: &manager.RetentionOptions{Count: &count},
		Compress:  &compress,
	}
	log := New(conf)
	log.SetLogAsynchronous(false)
	// each line exceeds 16 bytes, so the second write must rotate the file
	log.Info("first entry", nil)
	log.Info("second entry", nil)
	log.Close()
	if _, err := os.Stat(fileName + ".1"); err != nil {
		t.Errorf("expected a rotated archive per configured rotation: %v", err)
	}
}

func TestNewLoggerDebug_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.debug.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	logger.Debug("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}

func TestNewLoggerWarn_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.warn.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "warn"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	logger.Warn("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}

func TestNewLoggerWarnOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.warnonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "info"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	key := "warnonce-test-key"

	if logger.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", false, true)
	}

	ok := logger.WarnOnce(key, "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	if !logger.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", true, false)
	}

	ok = logger.WarnOnce(key, "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if !logger.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", true, false)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
	os.Remove(fileName)
}

func TestNewLoggerError_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.error.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "error"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	logger.Error("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}

func TestNewLoggerErrorOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.erroronce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "x"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)

	ok := logger.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = logger.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}

func TestNewLoggerDefault_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.info.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "x"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	logger.Info("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}

func TestNewLoggerInfoOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.infoonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "info"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	ok := logger.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = logger.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}

	logger.Close()
}

func TestNewLoggerFatal_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.fatal.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := New(conf)
	logger.Info("before fatal", nil)
	logger.Fatal(-1, "test entry", Pairs{"testKey": "testVal"})
	b, err := os.ReadFile(fileName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "test entry") {
		t.Errorf("fatal line was not flushed: %q", string(b))
	}
	if !strings.Contains(string(b), "before fatal") {
		t.Errorf("preceding asynchronous line was not flushed: %q", string(b))
	}
	logger.Close()
}

func TestSetLogLevel(t *testing.T) {
	l := ConsoleLogger(level.Info)
	if l.Level() != level.Info {
		t.Errorf("expected %s got %s", "info", l.Level())
	}

	l.SetLogLevel("warn")
	if l.Level() != "warn" {
		t.Errorf("expected %s got %s", "warn", l.Level())
	}
}

func TestStreamLogger(t *testing.T) {
	w := httptest.NewRecorder()
	sl := StreamLogger(w, "ERROR")
	sl.SetLogAsynchronous(false)
	sl.Error("test error", Pairs{"testKey": "testVal"})
	if w.Body.String() == "" {
		t.Error("expected non-empty string")
	}
}

func Benchmark_logOnce(b *testing.B) {
	fileName := b.TempDir() + "/out.once.bench.log"
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	b.ResetTimer()
	for b.Loop() {
		logger.InfoOnce("bench-test-key", "test entry", Pairs{"testKey": "testVal"})
	}
}

func Benchmark_Info(b *testing.B) {
	fileName := b.TempDir() + "/out.info.bench.log"
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := New(conf)
	logger.SetLogAsynchronous(false)
	b.ResetTimer()
	for b.Loop() {
		logger.Info("test entry", Pairs{"testKey": "testVal", "testkey2": "testVal2"})
	}
}
