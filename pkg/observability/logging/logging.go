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

// Package logging provides logging functionality to Trickster
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"

	gkl "github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/go-stack/stack"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// Logger is a container for the underlying log provider
type Logger struct {
	baseLogger gkl.Logger // the logger prior to leveling, used to relevel in config reload
	logger     gkl.Logger // the logger after leveling, which is used by importing packages
	closer     io.Closer
	level      string

	onceMutex      *sync.Mutex
	mtx            sync.Mutex
	onceRanEntries map[string]interface{}
}

// SyncLogger is a Logger that writes synchronously
type SyncLogger struct {
	*Logger
}

func Debug(logger interface{}, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		go l.Debug(event, detail)
	case *SyncLogger:
		l.Debug(event, detail)
	case *log.Logger:
		go l.Print("")
	case gkl.Logger:
		go level.Debug(l).Log(detail.ToList(event)...)
	}
}

func Info(logger interface{}, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		go l.Info(event, detail)
	case *SyncLogger:
		l.Info(event, detail)
	case *log.Logger:
		go l.Print("")
	case gkl.Logger:
		go level.Info(l).Log(detail.ToList(event)...)
	}
}

func Warn(logger interface{}, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		go l.Warn(event, detail)
	case *SyncLogger:
		l.Warn(event, detail)
	case *log.Logger:
		go l.Print("")
	case gkl.Logger:
		go level.Warn(l).Log(detail.ToList(event)...)
	}
}

func WarnOnce(logger interface{}, key string, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger: // must  be Synchronous to avoid double writes
		l.WarnOnce(key, event, detail)
	case *SyncLogger: // must  be Synchronous to avoid double writes
		l.WarnOnce(key, event, detail)
	case *log.Logger:
		go l.Print("")
	case gkl.Logger:
		go level.Warn(l).Log(detail.ToList(event)...)
	}
}

func Error(logger interface{}, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		go l.Error(event, detail)
	case *SyncLogger:
		l.Error(event, detail)
	case *log.Logger:
		go l.Print("")
	case gkl.Logger:
		go level.Error(l).Log(detail.ToList(event)...)
	}
}

func ErrorSynchronous(logger interface{}, event string, detail Pairs) {
	if logger == nil {
		return
	}
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		l.Error(event, detail)
	case *SyncLogger:
		l.Error(event, detail)
	case *log.Logger:
		l.Print("")
	case gkl.Logger:
		level.Error(l).Log(detail.ToList(event)...)
	}
}

// Fatal sends a "FATAL" event to the Logger and exits the program with the provided exit code
func Fatal(logger interface{}, code int, event string, detail Pairs) {
	// go-kit/log/level does not support Fatal, so implemented separately here
	detail["level"] = "fatal"
	detail["caller"] = pkgCaller{stack.Caller(1)}
	switch l := logger.(type) {
	case *Logger:
		l.Fatal(code, event, detail)
	case *SyncLogger:
		l.Fatal(code, event, detail)
	case *log.Logger:
		l.Print("")
	case gkl.Logger:
		level.Error(l).Log(detail.ToList(event)...)
	}
	if code >= 0 {
		os.Exit(code)
	}
}

func (p Pairs) ToList(event string) []interface{} {
	a := make([]interface{}, (len(p)*2)+2)
	var i int
	// Ensure the log level is the first Pair in the output order (after prefixes)
	if level, ok := p["level"]; ok {
		a[0] = "level"
		a[1] = level
		i += 2
	}
	// Ensure the event description is the second Pair in the output order (after prefixes)
	a[i] = "event"
	a[i+1] = event
	i += 2
	for k, v := range p {
		if k == "level" {
			continue
		}
		a[i] = k
		a[i+1] = v
		i += 2
	}
	return a
}

// DefaultLogger returns the default logger, which is the console logger at level "info"
func DefaultLogger() *Logger {
	return ConsoleLogger("info")
}

func noopLogger() *Logger {
	return &Logger{
		onceRanEntries: make(map[string]interface{}),
		onceMutex:      &sync.Mutex{},
	}
}

func StreamLogger(w io.Writer, logLevel string) *Logger {
	l := noopLogger()
	l.baseLogger = gkl.NewLogfmtLogger(gkl.NewSyncWriter(w))
	l.baseLogger = gkl.With(l.baseLogger,
		"time", gkl.DefaultTimestampUTC,
		"app", "trickster",
	)
	l.SetLogLevel(logLevel)
	return l
}

// ConsoleLogger returns a Logger object that prints log events to the Console
func ConsoleLogger(logLevel string) *Logger {

	l := noopLogger()
	wr := os.Stdout
	l.baseLogger = gkl.NewLogfmtLogger(gkl.NewSyncWriter(wr))
	l.baseLogger = gkl.With(l.baseLogger,
		"time", gkl.DefaultTimestampUTC,
		"app", "trickster",
	)
	l.SetLogLevel(logLevel)
	return l
}

// SetLogLevel sets the log level, defaulting to "Info" if the provided level is unknown
func (tl *Logger) SetLogLevel(logLevel string) {
	tl.level = strings.ToLower(logLevel)
	// wrap logger depending on log level
	switch tl.level {
	case "debug":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowDebug())
	case "info":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowInfo())
	case "warn":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowWarn())
	case "error":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowError())
	case "trace":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowDebug())
	case "none":
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowNone())
	default:
		tl.logger = level.NewFilter(tl.baseLogger, level.AllowInfo())
	}
}

// New returns a Logger for the provided logging configuration. The
// returned Logger will write to files distinguished from other Loggers by the
// instance string.
func New(conf *config.Config) *Logger {

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

	l.baseLogger = gkl.NewLogfmtLogger(gkl.NewSyncWriter(wr))
	l.baseLogger = gkl.With(l.baseLogger,
		"time", gkl.DefaultTimestampUTC,
		"app", "trickster",
	)

	l.SetLogLevel(conf.Logging.LogLevel)

	if c, ok := wr.(io.Closer); ok && c != nil {
		l.closer = c
	}

	return l
}

// Pairs represents a key=value pair that helps to describe a log event
type Pairs map[string]interface{}

// Info sends an "INFO" event to the Logger
func (tl *Logger) Info(event string, detail Pairs) {
	tl.mtx.Lock()
	level.Info(tl.logger).Log(detail.ToList(event)...)
	tl.mtx.Unlock()
}

// InfoOnce sends a "INFO" event to the Logger only once per key.
// Returns true if this invocation was the first, and thus sent to the Logger
func (tl *Logger) InfoOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "info." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = nil
		tl.Info(event, detail)
		return true
	}
	return false
}

// Warn sends an "WARN" event to the Logger
func (tl *Logger) Warn(event string, detail Pairs) {
	tl.mtx.Lock()
	level.Warn(tl.logger).Log(detail.ToList(event)...)
	tl.mtx.Unlock()
}

// WarnOnce sends a "WARN" event to the Logger only once per key.
// Returns true if this invocation was the first, and thus sent to the Logger
func (tl *Logger) WarnOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "warn." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = nil
		tl.Warn(event, detail)
		return true
	}
	return false
}

// HasWarnedOnce returns true if a warning for the key has already been sent to the Logger
func (tl *Logger) HasWarnedOnce(key string) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "warn." + key
	_, ok := tl.onceRanEntries[key]
	return ok
}

// Error sends an "ERROR" event to the Logger
func (tl *Logger) Error(event string, detail Pairs) {
	tl.mtx.Lock()
	level.Error(tl.logger).Log(detail.ToList(event)...)
	tl.mtx.Unlock()
}

// ErrorOnce sends an "ERROR" event to the Logger only once per key
// Returns true if this invocation was the first, and thus sent to the Logger
func (tl *Logger) ErrorOnce(key string, event string, detail Pairs) bool {
	tl.onceMutex.Lock()
	defer tl.onceMutex.Unlock()
	key = "error." + key
	if _, ok := tl.onceRanEntries[key]; !ok {
		tl.onceRanEntries[key] = nil
		tl.Error(event, detail)
		return true
	}
	return false
}

// Debug sends an "DEBUG" event to the Logger
func (tl *Logger) Debug(event string, detail Pairs) {
	tl.mtx.Lock()
	level.Debug(tl.logger).Log(detail.ToList(event)...)
	tl.mtx.Unlock()
}

// Trace sends a "TRACE" event to the Logger
func (tl *Logger) Trace(event string, detail Pairs) {
	tl.mtx.Lock()
	// go-kit/log/level does not support Trace, so implemented separately here
	if tl.level == "trace" {
		detail["level"] = "trace"
		tl.logger.Log(detail.ToList(event)...)
	}
	tl.mtx.Unlock()
}

// Fatal sends a "FATAL" event to the Logger and exits the program with the provided exit code
func (tl *Logger) Fatal(code int, event string, detail Pairs) {
	// go-kit/log/level does not support Fatal, so implemented separately here
	detail["level"] = "fatal"
	tl.logger.Log(detail.ToList(event)...)
	if code >= 0 {
		os.Exit(code)
	}
}

// Level returns the configured Log Level
func (tl *Logger) Level() string {
	return tl.level
}

// Close closes any opened file handles that were used for logging.
func (tl *Logger) Close() {
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
	return strings.TrimPrefix(fmt.Sprintf("%+v", pc.c), "github.com/trickstercache/trickster/pkg/")
}
