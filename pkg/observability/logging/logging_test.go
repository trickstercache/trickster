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
	"log"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/options"

	gkl "github.com/go-kit/log"
)

func TestConsoleLogger(t *testing.T) {

	testCases := []string{
		"debug",
		"info",
		"warn",
		"error",
		"trace",
		"none",
	}
	// it should create a logger for each level
	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			l := ConsoleLogger(tc)
			if l.level != tc {
				t.Errorf("mismatch in log level: expected=%s actual=%s", tc, l.level)
			}
		})
	}
}

func TestNew(t *testing.T) {
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogLevel: "info"}
	logger := New(conf)
	if logger.level != "info" {
		t.Errorf("expected %s got %s", "info", logger.level)
	}
	logger.Close()
}

func TestNewLogger_LogFile(t *testing.T) {
	td := t.TempDir()
	fileName := td + "/out.log"
	instanceFileName := td + "/out.1.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 1}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "info"}
	logger := &SyncLogger{Logger: New(conf)}
	Info(logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(instanceFileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerDebug_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.debug.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Debug("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerWarn_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.warn.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "warn"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Warn("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerWarnOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.warnonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "x"}
	logger := &SyncLogger{Logger: New(conf)}

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
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerError_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.error.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "error"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Error("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerErrorOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.erroronce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "x"}
	logger := &SyncLogger{Logger: New(conf)}

	ok := logger.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = logger.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerTrace_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.trace.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "trace"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Trace("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerDefault_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.info.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "x"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Info("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestNewLoggerInfoOnce_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.infoonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "info"}
	logger := &SyncLogger{Logger: New(conf)}
	ok := logger.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = logger.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}

	logger.Close()
}

func TestNewLoggerFatal_LogFile(t *testing.T) {
	fileName := t.TempDir() + "/out.fatal.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &options.Options{LogFile: fileName, LogLevel: "debug"}
	logger := &SyncLogger{Logger: New(conf)}
	logger.Fatal(-1, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
}

func TestSetLogLevel(t *testing.T) {

	l := DefaultLogger()
	if l.level != "info" {
		t.Errorf("expected %s got %s", "info", l.level)
	}

	l.SetLogLevel("warn")
	if l.Level() != "warn" {
		t.Errorf("expected %s got %s", "warn", l.level)
	}

}

func TestDebug(t *testing.T) {
	testLogFunction(Debug, nil, nil, "DEBUG", t)
}

func TestInfo(t *testing.T) {
	testLogFunction(Info, nil, nil, "INFO", t)
}

func TestWarn(t *testing.T) {
	testLogFunction(Warn, WarnOnce, nil, "WARN", t)
}

func TestError(t *testing.T) {
	testLogFunction(Error, nil, nil, "ERROR", t)
}

func TestFatal(t *testing.T) {
	testLogFunction(nil, nil, Fatal, "ERROR", t)
}

type basicLogFunc func(interface{}, string, Pairs)
type onceLogFunc func(interface{}, string, string, Pairs)
type fatalLogFunc func(interface{}, int, string, Pairs)

func testLogFunction(f1 basicLogFunc, f2 onceLogFunc, f3 fatalLogFunc,
	level string, t *testing.T) {

	tw := httptest.NewRecorder()
	gw := httptest.NewRecorder()
	kw := httptest.NewRecorder()
	sw := httptest.NewRecorder()

	tl := ConsoleLogger(level)
	tl.logger = gkl.NewJSONLogger(tw)

	sla := ConsoleLogger(level)
	sla.logger = gkl.NewJSONLogger(sw)

	sl := &SyncLogger{Logger: sla}
	gl := log.New(gw, "", 0)
	kl := gkl.NewJSONLogger(kw)

	loggers := []interface{}{nil, tl, sl, gl, kl}

	// cover debug cases
	for _, logger := range loggers {

		if f1 != nil {
			f1(logger, "test trickster "+level, Pairs{"testKey": "testValue"})
		}
		if f2 != nil {
			f2(logger, "test-key", "test trickster "+level, Pairs{"testKey": "testValue"})
		}
		if f3 != nil {
			f3(logger, -1, "test trickster "+level, Pairs{"testKey": "testValue"})
		}
	}
	time.Sleep(time.Millisecond * 300)

	if tw.Body.String() == "" {
		t.Error("expected non-empty string")
	}

	if gw.Body.String() == "" {
		t.Error("expected non-empty string")
	}

	if kw.Body.String() == "" {
		t.Error("expected non-empty string")
	}

	if sw.Body.String() == "" {
		t.Error("expected non-empty string")
	}

}

func TestStreamLogger(t *testing.T) {

	w := httptest.NewRecorder()
	sl := StreamLogger(w, "ERROR")
	sl.Error("test error", Pairs{"testKey": "testVal"})
	if w.Body.String() == "" {
		t.Error("expected non-empty string")
	}

}
