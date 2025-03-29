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

func SetLogLevel(logLevel level.Level) {
	logger.SetLogLevel(logLevel)
}

func SetLogAsynchronous(asyncEnabled bool) {
	logger.SetLogAsynchronous(asyncEnabled)
}

func Level() level.Level {
	return logger.Level()
}

func Log(logLevel level.Level, event string, detail logging.Pairs) {
	logger.Log(logLevel, event, detail)
}

func Debug(event string, detail logging.Pairs) {
	logger.Debug(event, detail)
}

func Info(event string, detail logging.Pairs) {
	logger.Info(event, detail)
}

func Warn(event string, detail logging.Pairs) {
	logger.Warn(event, detail)
}

func Error(event string, detail logging.Pairs) {
	logger.Error(event, detail)
}

func Fatal(code int, event string, detail logging.Pairs) {
	logger.Fatal(code, event, detail)
}

func LogSynchronous(logLevel level.Level, event string, detail logging.Pairs) {
	logger.LogSynchronous(logLevel, event, detail)
}

func DebugSynchronous(event string, detail logging.Pairs) {
	logger.DebugSynchronous(event, detail)
}

func InfoSynchronous(event string, detail logging.Pairs) {
	logger.InfoSynchronous(event, detail)
}

func WarnSynchronous(event string, detail logging.Pairs) {
	logger.WarnSynchronous(event, detail)
}

func ErrorSynchronous(event string, detail logging.Pairs) {
	logger.ErrorSynchronous(event, detail)
}

func LogOnce(logLevel level.Level, key, event string, detail logging.Pairs) bool {
	return logger.LogOnce(logLevel, key, event, detail)
}

func DebugOnce(key, event string, detail logging.Pairs) bool {
	return logger.DebugOnce(key, event, detail)
}

func InfoOnce(key, event string, detail logging.Pairs) bool {
	return logger.InfoOnce(key, event, detail)
}

func WarnOnce(key, event string, detail logging.Pairs) bool {
	return logger.WarnOnce(key, event, detail)
}
func ErrorOnce(key, event string, detail logging.Pairs) bool {
	return logger.ErrorOnce(key, event, detail)
}

func HasDebuggedOnce(key string) bool {
	return logger.HasDebuggedOnce(key)
}

func HasInfoedOnce(key string) bool {
	return logger.HasInfoedOnce(key)
}

func HasWarnedOnce(key string) bool {
	return logger.HasWarnedOnce(key)
}

func HasErroredOnce(key string) bool {
	return logger.HasErroredOnce(key)
}

func HasLoggedOnce(logLevel level.Level, key string) bool {
	return logger.HasLoggedOnce(logLevel, key)
}
