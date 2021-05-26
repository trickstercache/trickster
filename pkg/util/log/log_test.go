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

package log

import (
	"os"
	"testing"

	"github.com/trickstercache/trickster/pkg/config"
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
	conf.Logging = &config.LoggingConfig{LogLevel: "info"}
	log := New(conf)
	defer log.Close()
	if log.level != "info" {
		t.Errorf("expected %s got %s", "info", log.level)
	}
}

func TestNewLogger_LogFile(t *testing.T) {
	fileName := "out.log"
	instanceFileName := "out.1.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 1}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "info"}
	log := New(conf)
	defer log.Close()
	log.Info("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(instanceFileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(instanceFileName)
}

func TestNewLoggerDebug_LogFile(t *testing.T) {
	fileName := "out.debug.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	log := New(conf)
	defer log.Close()
	log.Debug("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerWarn_LogFile(t *testing.T) {
	fileName := "out.warn.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "warn"}
	log := New(conf)
	defer log.Close()
	log.Warn("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerWarnOnce_LogFile(t *testing.T) {
	fileName := "out.warnonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "x"}
	log := New(conf)
	defer log.Close()

	key := "warnonce-test-key"

	if log.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", false, true)
	}

	ok := log.WarnOnce(key, "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	if !log.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", true, false)
	}

	ok = log.WarnOnce(key, "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if !log.HasWarnedOnce(key) {
		t.Errorf("expected %t got %t", true, false)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerError_LogFile(t *testing.T) {
	fileName := "out.error.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "error"}
	log := New(conf)
	defer log.Close()
	log.Error("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerErrorOnce_LogFile(t *testing.T) {
	fileName := "out.erroronce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "x"}
	log := New(conf)
	defer log.Close()

	ok := log.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = log.ErrorOnce("erroroonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerTrace_LogFile(t *testing.T) {
	fileName := "out.trace.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "trace"}
	log := New(conf)
	defer log.Close()
	log.Trace("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerDefault_LogFile(t *testing.T) {
	fileName := "out.info.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "x"}
	log := New(conf)
	defer log.Close()
	log.Info("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerInfoOnce_LogFile(t *testing.T) {
	fileName := "out.infoonce.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "info"}
	log := New(conf)
	defer log.Close()
	ok := log.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if !ok {
		t.Errorf("expected %t got %t", true, ok)
	}

	ok = log.InfoOnce("infoonce-test-key", "test entry", Pairs{"testKey": "testVal"})
	if ok {
		t.Errorf("expected %t got %t", false, ok)
	}

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}

	log.Close()
	os.Remove(fileName)
}

func TestNewLoggerFatal_LogFile(t *testing.T) {
	fileName := "out.fatal.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	log := New(conf)
	defer log.Close()
	log.Fatal(-1, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
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
