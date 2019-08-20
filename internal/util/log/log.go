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
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Comcast/trickster/internal/config"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-stack/stack"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

func mapToArray(event string, detail Pairs) []interface{} {
	a := make([]interface{}, (len(detail)*2)+2)
	var i int

	// Ensure the log level is the first Pair in the output order (after prefixes)
	if level, ok := detail["level"]; ok {
		a[0] = "level"
		a[1] = level
		delete(detail, "level")
		i += 2
	}

	// Ensure the event description is the second Pair in the output order (after prefixes)
	a[i] = "event"
	a[i+1] = event
	i += 2

	for k, v := range detail {
		a[i] = k
		a[i+1] = v
		i += 2
	}
	return a
}

// ConsoleLogger returns a TricksterLogger object that prints log events to the Console
func ConsoleLogger(logLevel string) *TricksterLogger {
	l := &TricksterLogger{}

	wr := os.Stdout

	logger := log.NewLogfmtLogger(log.NewSyncWriter(wr))
	logger = log.With(logger,
		"time", log.DefaultTimestampUTC,
		"app", "trickster",
		"caller", log.Valuer(func() interface{} {
			return pkgCaller{stack.Caller(6)}
		}),
	)

	l.level = strings.ToLower(logLevel)

	// wrap logger depending on log level
	switch l.level {
	case "debug":
		logger = level.NewFilter(logger, level.AllowDebug())
	case "info":
		logger = level.NewFilter(logger, level.AllowInfo())
	case "warn":
		logger = level.NewFilter(logger, level.AllowWarn())
	case "error":
		logger = level.NewFilter(logger, level.AllowError())
	case "trace":
		logger = level.NewFilter(logger, level.AllowDebug())
	default:
		logger = level.NewFilter(logger, level.AllowInfo())
	}

	logger = level.NewFilter(logger, level.AllowInfo())

	l.logger = logger

	return l
}

// New returns a TricksterLogger for the provided logging configuration. The
// returned TricksterLogger will write to files distinguished from other TricksterLoggers by the
// instance string.
func New(config *config.LoggingConfig, instanceID int) *TricksterLogger {
	l := &TricksterLogger{}

	var wr io.Writer

	if config.LogFile == "" {
		wr = os.Stdout
	} else {
		logFile := config.LogFile
		if instanceID > 0 {
			logFile = strings.Replace(logFile, ".log", "."+strconv.Itoa(instanceID)+".log", 1)
		}

		wr = &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    256,  // megabytes
			MaxBackups: 80,   // 256 megs @ 80 backups is 20GB of Logs
			MaxAge:     7,    // days
			Compress:   true, // Compress Rolled Backups
		}
	}

	logger := log.NewLogfmtLogger(log.NewSyncWriter(wr))
	logger = log.With(logger,
		"time", log.DefaultTimestampUTC,
		"app", "trickster",
		"caller", log.Valuer(func() interface{} {
			return pkgCaller{stack.Caller(6)}
		}),
	)

	l.level = strings.ToLower(config.LogLevel)

	// wrap logger depending on log level
	switch l.level {
	case "debug":
		logger = level.NewFilter(logger, level.AllowDebug())
	case "info":
		logger = level.NewFilter(logger, level.AllowInfo())
	case "warn":
		logger = level.NewFilter(logger, level.AllowWarn())
	case "error":
		logger = level.NewFilter(logger, level.AllowError())
	case "trace":
		logger = level.NewFilter(logger, level.AllowDebug())
	default:
		logger = level.NewFilter(logger, level.AllowInfo())
	}

	l.logger = logger
	if c, ok := wr.(io.Closer); ok && c != nil {
		l.closer = c
	}

	return l

}

// Pairs represents a key=value pair that helps to describe a log event
type Pairs map[string]interface{}

// TricksterLogger is a container for the underlying log provider
type TricksterLogger struct {
	logger log.Logger
	closer io.Closer
	level  string
}

// Info sends an "INFO" event to the TricksterLogger
func Info(logger log.Logger, event string, detail Pairs) {
	level.Info(logger).Log(mapToArray(event, detail)...)
}

// Warn sends an "WARN" event to the TricksterLogger
func Warn(logger log.Logger, event string, detail Pairs) {
	level.Warn(logger).Log(mapToArray(event, detail)...)
}

// Error sends an "ERROR" event to the TricksterLogger
func Error(logger log.Logger, event string, detail Pairs) {
	level.Error(logger).Log(mapToArray(event, detail)...)
}

// Debug sends an "DEBUG" event to the TricksterLogger
func Debug(logger log.Logger, event string, detail Pairs) {
	level.Debug(logger).Log(mapToArray(event, detail)...)
}

// Trace sends a "TRACE" event to the TricksterLogger
func (l *TricksterLogger) Trace(event string, detail Pairs) {
	// go-kit/log/level does not support Trace, so implemented separately here
	if l.level == "trace" {
		detail["level"] = "trace"
		l.logger.Log(mapToArray(event, detail)...)
	}
}

// Fatal sends a "FATAL" event to the TricksterLogger and exits the program with the provided exit code
func (l *TricksterLogger) Fatal(code int, event string, detail Pairs) {
	// go-kit/log/level does not support Fatal, so implemented separately here
	detail["level"] = "fatal"
	l.logger.Log(mapToArray(event, detail)...)
	os.Exit(code)
}

// Close closes any opened file handles that were used for logging.
func (l *TricksterLogger) Close() {
	if l.closer != nil {
		l.closer.Close()
	}
}

// Log implements log.Logger interface
func (l *TricksterLogger) Log(keyvals ...interface{}) error {
	return l.logger.Log(keyvals...)
}

// pkgCaller wraps a stack.Call to make the default string output include the
// package path.
type pkgCaller struct {
	c stack.Call
}

// String returns a path from the call stack that is relative to the root of the project
func (pc pkgCaller) String() string {
	return strings.TrimPrefix(fmt.Sprintf("%+v", pc.c), "github.com/Comcast/trickster/internal/")
}
