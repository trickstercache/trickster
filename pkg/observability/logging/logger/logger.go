//go:build !race

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

// package logger provides a package-level logger for application-wide use,
// and provides all of the same functions of logging.Logger at the package
// level - except for Close() (because this logger should always be open).
// By default, the logger is a Console Logger @ INFO. Use SetLogger() to
// set the Logger object to any logging.Logger.
package logger

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
)

var logger logging.Logger = logging.ConsoleLogger(level.Info)

// Logger returns gthe Package-level Logger
func Logger() logging.Logger {
	return logger
}

// SetLogger sets the package-level logger object
func SetLogger(l logging.Logger) {
	if l == nil {
		return
	}
	logger = l
}

// SetLogLevel sets the log level for the package-level logger
func SetLogLevel(logLevel level.Level) {
	logger.SetLogLevel(logLevel)
}

// SetLogAsynchronous indicates whether log calls should be made synchronously
// (blocking) or asynchronously (non-blocking).
func SetLogAsynchronous(asyncEnabled bool) {
	logger.SetLogAsynchronous(asyncEnabled)
}

// Level returns the current Log Level for the package-level logger
func Level() level.Level {
	return logger.Level()
}

// Log logs an event to the package-level logger
func Log(logLevel level.Level, event string, detail logging.Pairs) {
	logger.Log(logLevel, event, detail)
}

// Debug logs a DEBUG event to the package-level logger
func Debug(event string, detail logging.Pairs) {
	logger.Debug(event, detail)
}

// Info logs an INFO event to the package-level logger
func Info(event string, detail logging.Pairs) {
	logger.Info(event, detail)
}

// Warn logs a WARN event to the package-level logger
func Warn(event string, detail logging.Pairs) {
	logger.Warn(event, detail)
}

// Error logs an ERROR event to the package-level logger
func Error(event string, detail logging.Pairs) {
	logger.Error(event, detail)
}

// Fatal logs a FATAL event to the package-level logger and exits the process
// with the provided exit code
func Fatal(code int, event string, detail logging.Pairs) {
	logger.Fatal(code, event, detail)
}

// LogSynchronous logs an event to the package-level logger
// synchronously even if LogAsynchronous is true
func LogSynchronous(logLevel level.Level, event string, detail logging.Pairs) {
	logger.LogSynchronous(logLevel, event, detail)
}

// Debug logs a DEBUG event to the package-level logger
// synchronously even if LogAsynchronous is true
func DebugSynchronous(event string, detail logging.Pairs) {
	logger.DebugSynchronous(event, detail)
}

// Info logs an INFO event to the package-level logger
// synchronously even if LogAsynchronous is true
func InfoSynchronous(event string, detail logging.Pairs) {
	logger.InfoSynchronous(event, detail)
}

// Warn logs a WARN event to the package-level logger
// synchronously even if LogAsynchronous is true
func WarnSynchronous(event string, detail logging.Pairs) {
	logger.WarnSynchronous(event, detail)
}

// Error logs an ERROR event to the package-level logger
// synchronously even if LogAsynchronous is true
func ErrorSynchronous(event string, detail logging.Pairs) {
	logger.ErrorSynchronous(event, detail)
}

// LogOnce logs an event to the package-level logger
// only once based on unique key
func LogOnce(logLevel level.Level, key, event string, detail logging.Pairs) bool {
	return logger.LogOnce(logLevel, key, event, detail)
}

// Debug logs a DEBUG event to the package-level logger
// only once based on unique key
func DebugOnce(key, event string, detail logging.Pairs) bool {
	return logger.DebugOnce(key, event, detail)
}

// Info logs an INFO event to the package-level logger
// only once based on unique key
func InfoOnce(key, event string, detail logging.Pairs) bool {
	return logger.InfoOnce(key, event, detail)
}

// Warn logs a WARN event to the package-level logger
// only once based on unique key
func WarnOnce(key, event string, detail logging.Pairs) bool {
	return logger.WarnOnce(key, event, detail)
}

// Error logs an ERROR event to the package-level logger
// only once based on unique key
func ErrorOnce(key, event string, detail logging.Pairs) bool {
	return logger.ErrorOnce(key, event, detail)
}

// HasDebuggedOnce returns true if a Debug event for the provided key has been logged
func HasDebuggedOnce(key string) bool {
	return logger.HasDebuggedOnce(key)
}

// HasInfoedOnce returns true if an Info event for the provided key has been logged
func HasInfoedOnce(key string) bool {
	return logger.HasInfoedOnce(key)
}

// HasWarnedOnce returns true if a Warn event for the provided key has been logged
func HasWarnedOnce(key string) bool {
	return logger.HasWarnedOnce(key)
}

// HasErroredOnce returns true if an Error event for the provided key has been logged
func HasErroredOnce(key string) bool {
	return logger.HasErroredOnce(key)
}

// HasLoggedOnce returns true if an event for the provided key has been logged
// at the provided level
func HasLoggedOnce(logLevel level.Level, key string) bool {
	return logger.HasLoggedOnce(logLevel, key)
}
