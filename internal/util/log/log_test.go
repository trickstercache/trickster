/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/config"
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

func TestInit(t *testing.T) {

	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogLevel: "info"}
	logger := New(config.Logging, 0)
	if logger.level != "info" {
		t.Errorf("expected %s got %s", "info", logger.level)
	}
}

func TestNewLogger_LogFile(t *testing.T) {
	fileName := "out.log"
	instanceFileName := "out.1.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "info"}
	logger := New(config.Logging, 1)
	Info(logger.logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(instanceFileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(instanceFileName)
}

func TestNewLoggerDebug_LogFile(t *testing.T) {
	fileName := "out.debug.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	logger := New(config.Logging, 0)
	Debug(logger.logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}

func TestNewLoggerWarn_LogFile(t *testing.T) {
	fileName := "out.warn.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "warn"}
	logger := New(config.Logging, 0)
	Warn(logger.logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}

func TestNewLoggerError_LogFile(t *testing.T) {
	fileName := "out.error.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "error"}
	logger := New(config.Logging, 0)
	Error(logger.logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}

func TestNewLoggerTrace_LogFile(t *testing.T) {
	fileName := "out.trace.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "trace"}
	logger := New(config.Logging, 0)
	logger.Trace("test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}

func TestNewLoggerDefault_LogFile(t *testing.T) {
	fileName := "out.info.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "x"}
	logger := New(config.Logging, 0)
	Info(logger.logger, "test entry", Pairs{"testKey": "testVal"})
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}
