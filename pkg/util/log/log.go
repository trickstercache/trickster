/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package log provides logging functionality to Trickster
package log

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/tricksterproxy/trickster/pkg/config"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-stack/stack"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Logger is the handle to the common TricksterLogger
// var Logger *TricksterLogger

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

// DefaultLogger returns the default logger, which is the console logger at level "info"
func DefaultLogger() *TricksterLogger {
	return ConsoleLogger("info")
}

func noopLogger() *TricksterLogger {
	return &TricksterLogger{
		onceRanEntries: make(map[string]bool),
		onceMutex:      &sync.Mutex{},
	}
}

// ConsoleLogger returns a TricksterLogger object that prints log events to the Console
func ConsoleLogger(logLevel string) *TricksterLogger {
	l := noopLogger()

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
	case "none":
		logger = level.NewFilter(logger, level.AllowNone())
	default:
		logger = level.NewFilter(logger, level.AllowInfo())
	}

	l.logger = logger

	return l
}

// Init returns a TricksterLogger for the provided logging configuration. The
// returned TricksterLogger will write to files distinguished from other TricksterLoggers by the
// instance string.
func Init(conf *config.TricksterConfig) *TricksterLogger {

	l := noopLogger()
	var wr io.Writer

	if conf.Logging.LogFile == "" {
		wr = os.Stdout
	} else {
		logFile := conf.Logging.LogFile
		if conf.Main.InstanceID > 0 {
			logFile = strings.Replace(logFile, ".log", "."+strconv.Itoa(conf.Main.InstanceID)+".log", 1)
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

	l.level = strings.ToLower(conf.Logging.LogLevel)

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

	onceMutex      *sync.Mutex
	onceRanEntries map[string]bool
}

// Info sends an "INFO" event to the TricksterLogger
func (tl *TricksterLogger) Info(event string, detail Pairs) {
	level.Info(tl.logger).Log(mapToArray(event, detail)...)
}

// InfoOnce sends a "INFO" event to the TricksterLogger only once per key.
// Returns true if this invocation was the first, and thus sent to the TricksterLogger
func (tl *TricksterLogger) InfoOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "info." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = true
		tl.Info(event, detail)
		return true
	}
	return false
}

// Warn sends an "WARN" event to the TricksterLogger
func (tl *TricksterLogger) Warn(event string, detail Pairs) {
	level.Warn(tl.logger).Log(mapToArray(event, detail)...)
}

// WarnOnce sends a "WARN" event to the TricksterLogger only once per key.
// Returns true if this invocation was the first, and thus sent to the TricksterLogger
func (tl *TricksterLogger) WarnOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "warn." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = true
		tl.Warn(event, detail)
		return true
	}
	return false
}

// HasWarnedOnce returns true if a warning for the key has already been sent to the TricksterLogger
func (tl *TricksterLogger) HasWarnedOnce(key string) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "warn." + key
	_, ok := tl.onceRanEntries[key]
	return ok
}

// Error sends an "ERROR" event to the TricksterLogger
func (tl *TricksterLogger) Error(event string, detail Pairs) {
	level.Error(tl.logger).Log(mapToArray(event, detail)...)
}

// ErrorOnce sends an "ERROR" event to the TricksterLogger only once per key
// Returns true if this invocation was the first, and thus sent to the TricksterLogger
func (tl *TricksterLogger) ErrorOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "error." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = true
		tl.Error(event, detail)
		return true
	}
	return false
}

// Debug sends an "DEBUG" event to the TricksterLogger
func (tl *TricksterLogger) Debug(event string, detail Pairs) {
	level.Debug(tl.logger).Log(mapToArray(event, detail)...)
}

// Trace sends a "TRACE" event to the TricksterLogger
func (tl *TricksterLogger) Trace(event string, detail Pairs) {
	// go-kit/log/level does not support Trace, so implemented separately here
	if tl.level == "trace" {
		detail["level"] = "trace"
		tl.logger.Log(mapToArray(event, detail)...)
	}
}

// Fatal sends a "FATAL" event to the TricksterLogger and exits the program with the provided exit code
func (tl *TricksterLogger) Fatal(code int, event string, detail Pairs) {
	// go-kit/log/level does not support Fatal, so implemented separately here
	detail["level"] = "fatal"
	tl.logger.Log(mapToArray(event, detail)...)
	if code >= 0 {
		os.Exit(code)
	}
}

// Level returns the configured Log Level
func (tl *TricksterLogger) Level() string {
	return tl.level
}

// Close closes any opened file handles that were used for logging.
func (tl *TricksterLogger) Close() {
	if tl.closer != nil {
		tl.closer.Close()
	}
}

// pkgCaller wraps a stack.Call to make the default string output include the
// package path.
type pkgCaller struct {
	c stack.Call
}

// String returns a path from the call stack that is relative to the root of the project
func (pc pkgCaller) String() string {
	return strings.TrimPrefix(fmt.Sprintf("%+v", pc.c), "github.com/tricksterproxy/trickster/pkg/")
}
