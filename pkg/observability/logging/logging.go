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
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

var _ Logger = &logger{}
var _ io.Writer = &logger{}

type Logger interface {
	//
	SetLogLevel(level.Level)
	SetLogAsynchronous(bool)
	//
	Level() level.Level
	Close()
	//
	Log(logLevel level.Level, event string, detail Pairs)
	Debug(event string, detail Pairs)
	Info(event string, detail Pairs)
	Warn(event string, detail Pairs)
	Error(event string, detail Pairs)
	Fatal(code int, event string, detail Pairs)
	//
	// These funcs log synchronously even if the logger is set to Asynchronous
	LogSynchronous(logLevel level.Level, event string, detail Pairs)
	DebugSynchronous(event string, detail Pairs)
	InfoSynchronous(event string, detail Pairs)
	WarnSynchronous(event string, detail Pairs)
	ErrorSynchronous(event string, detail Pairs)
	//
	LogOnce(logLevel level.Level, key, event string, detail Pairs) bool
	DebugOnce(key, event string, detail Pairs) bool
	InfoOnce(key, event string, detail Pairs) bool
	WarnOnce(key, event string, detail Pairs) bool
	ErrorOnce(key, event string, detail Pairs) bool
	//
	HasLoggedOnce(logLevel level.Level, key string) bool
	HasDebuggedOnce(key string) bool
	HasInfoedOnce(key string) bool
	HasWarnedOnce(key string) bool
	HasErroredOnce(key string) bool
}

type logFunc func(level.Level, string, Pairs)

// Pairs represents a key=value pair that helps to describe a log event
type Pairs map[string]any

// New returns a Logger for the provided logging configuration. The
// returned Logger will write to files distinguished from other Loggers by the
// instance string.
func New(conf *config.Config) Logger {
	l := &logger{
		onceRanEntries: make(map[string]*sync.Once),
	}
	l.logFunc = l.logAsyncronous
	if conf.Logging.LogFile == "" {
		l.writer = os.Stdout
	} else {
		logFile := conf.Logging.LogFile
		if conf.Main.InstanceID > 0 {
			logFile = strings.Replace(logFile, ".log",
				"."+strconv.Itoa(conf.Main.InstanceID)+".log", 1)
		}
		l.writer = &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    256,  // megabytes
			MaxBackups: 80,   // 256 megs @ 80 backups is 20GB of Logs
			MaxAge:     7,    // days
			Compress:   true, // Compress Rolled Backups
		}
	}
	if c, ok := l.writer.(io.Closer); ok && c != nil {
		l.closer = c
	}
	l.SetLogLevel(level.Level(conf.Logging.LogLevel))
	return l
}

func NoopLogger() Logger {
	l := &logger{
		logFunc:        func(level.Level, string, Pairs) {},
		onceRanEntries: make(map[string]*sync.Once),
		levelID:        level.InfoID,
		level:          level.Info,
	}
	return l
}

func StreamLogger(w io.Writer, logLevel level.Level) Logger {
	l := &logger{
		writer:         w,
		onceRanEntries: make(map[string]*sync.Once),
	}
	l.logFunc = l.logAsyncronous

	if c, ok := l.writer.(io.Closer); ok && c != nil {
		l.closer = c
	}
	l.SetLogLevel(logLevel)
	return l
}

func ConsoleLogger(logLevel level.Level) Logger {
	l := &logger{
		writer:         os.Stdout,
		onceRanEntries: make(map[string]*sync.Once),
	}
	l.logFunc = l.logAsyncronous
	l.SetLogLevel(logLevel)
	return l
}

type logger struct {
	level          level.Level
	levelID        level.LevelID
	writer         io.Writer
	closer         io.Closer
	onceMutex      sync.Mutex
	mtx            sync.Mutex
	onceRanEntries map[string]*sync.Once
	logFunc        logFunc
}

func (l *logger) Write(b []byte) (int, error) {
	if l.writer == nil {
		return 0, nil
	}
	return l.writer.Write(b)
}

func (l *logger) SetLogLevel(logLevel level.Level) {
	id := level.GetLevelID(logLevel)
	if id == 0 {
		l.WarnOnce("loglevel."+string(logLevel),
			"unknown log level; using INFO",
			Pairs{"providedLevel": logLevel})
		logLevel = level.Info
		id = level.InfoID
	}
	l.level = logLevel
	l.levelID = id
}

func (l *logger) SetLogAsynchronous(asyncEnabled bool) {
	if asyncEnabled {
		l.logFunc = l.logAsyncronous
	} else {
		l.logFunc = l.log
	}
}

func (l *logger) Log(logLevel level.Level, event string, detail Pairs) {
	lid := level.GetLevelID(logLevel)
	if lid == 0 || lid < l.levelID {
		return
	}
	l.logFunc(logLevel, event, detail)
}

func (l *logger) logFuncConditionally(level level.Level, levelID level.LevelID, event string, detail Pairs) {
	if l.levelID > levelID {
		return
	}
	l.logFunc(level, event, detail)
}

func (l *logger) Debug(event string, detail Pairs) {
	l.logFuncConditionally(level.Debug, level.DebugID, event, detail)
}

func (l *logger) Info(event string, detail Pairs) {
	l.logFuncConditionally(level.Info, level.InfoID, event, detail)
}

func (l *logger) Warn(event string, detail Pairs) {
	l.logFuncConditionally(level.Warn, level.WarnID, event, detail)
}

func (l *logger) Error(event string, detail Pairs) {
	l.logFuncConditionally(level.Error, level.ErrorID, event, detail)
}

func (l *logger) LogSynchronous(logLevel level.Level, event string, detail Pairs) {
	lid := level.GetLevelID(logLevel)
	if lid == 0 || lid < l.levelID {
		return
	}
	l.log(logLevel, event, detail)

}

func (l *logger) logConditionally(level level.Level, levelID level.LevelID, event string, detail Pairs) {
	if l.levelID > levelID {
		return
	}
	l.log(level, event, detail)
}

func (l *logger) DebugSynchronous(event string, detail Pairs) {
	l.logConditionally(level.Debug, level.DebugID, event, detail)
}

func (l *logger) InfoSynchronous(event string, detail Pairs) {
	l.logConditionally(level.Info, level.InfoID, event, detail)
}

func (l *logger) WarnSynchronous(event string, detail Pairs) {
	l.logConditionally(level.Warn, level.WarnID, event, detail)
}

func (l *logger) ErrorSynchronous(event string, detail Pairs) {
	l.logConditionally(level.Error, level.ErrorID, event, detail)
}

func (l *logger) Fatal(code int, event string, detail Pairs) {
	l.log(level.Fatal, event, detail)
	if code < 0 {
		// tests will send a -1 code to avoid a panic during the test
		return
	}
	if code == 0 {
		code = 1
	}
	os.Exit(code)
}

func (l *logger) LogOnce(logLevel level.Level, key, event string, detail Pairs) bool {
	lid := level.GetLevelID(logLevel)
	return l.logOnce(logLevel, lid, key, event, detail)
}

func (l *logger) logOnce(logLevel level.Level, lid level.LevelID,
	key, event string, detail Pairs) bool {
	if lid == 0 || lid < l.levelID || l.HasLoggedOnce(logLevel, key) {
		return false
	}
	key = string(logLevel) + "." + key
	l.onceMutex.Lock()
	if l.onceRanEntries[key] == nil {
		l.onceRanEntries[key] = &sync.Once{}
	}
	var ok bool
	l.onceRanEntries[key].Do(func() {
		l.log(logLevel, event, detail)
		ok = true
	})
	l.onceMutex.Unlock()
	return ok
}

func (l *logger) DebugOnce(key, event string, detail Pairs) bool {
	return l.logOnce(level.Debug, level.DebugID, key, event, detail)
}

func (l *logger) InfoOnce(key, event string, detail Pairs) bool {
	return l.logOnce(level.Info, level.InfoID, key, event, detail)
}

func (l *logger) WarnOnce(key, event string, detail Pairs) bool {
	return l.logOnce(level.Warn, level.WarnID, key, event, detail)
}

func (l *logger) ErrorOnce(key, event string, detail Pairs) bool {
	return l.logOnce(level.Error, level.ErrorID, key, event, detail)
}

func (l *logger) HasDebuggedOnce(key string) bool {
	return l.HasLoggedOnce(level.Debug, key)
}

func (l *logger) HasInfoedOnce(key string) bool {
	return l.HasLoggedOnce(level.Info, key)
}

func (l *logger) HasWarnedOnce(key string) bool {
	return l.HasLoggedOnce(level.Warn, key)
}

func (l *logger) HasErroredOnce(key string) bool {
	return l.HasLoggedOnce(level.Error, key)
}

func (l *logger) HasLoggedOnce(logLevel level.Level, key string) bool {
	key = string(logLevel) + "." + key
	l.onceMutex.Lock()
	_, ok := l.onceRanEntries[key]
	l.onceMutex.Unlock()
	return ok
}

func (l *logger) logAsyncronous(logLevel level.Level, event string, detail Pairs) {
	go l.log(logLevel, event, detail)
}

const defaultLogItemCount = 4

func (l *logger) log(logLevel level.Level, event string, detail Pairs) {
	if l.writer == nil {
		return
	}
	ts := time.Now()
	ld := len(detail)
	keys := make([]string, defaultLogItemCount, ld+defaultLogItemCount)
	keys[0] = "time=" + ts.UTC().Format(time.RFC3339Nano)
	keys[1] = "app=trickster"
	keys[2] = "level=" + string(logLevel)
	if strings.HasPrefix(event, " ") || strings.HasSuffix(event, " ") {
		event = strings.TrimSpace(event)
	}
	keys[3] = "event=" + quoteAsNeeded(event)
	var i int
	if ld > 0 {
		sortedKeys := make([]string, ld)
		for k, v := range detail {
			if s, ok := v.(string); ok {
				v = quoteAsNeeded(s)
			} else if stringer, ok := v.(fmt.Stringer); ok {
				v = quoteAsNeeded(stringer.String())
			} else if err, ok := v.(error); ok {
				v = quoteAsNeeded(err.Error())
			}
			sortedKeys[i] = fmt.Sprintf("%s=%v", k, v)
			i++
		}
		slices.Sort(sortedKeys)
		keys = append(keys, sortedKeys...)
	}
	l.mtx.Lock()
	l.writer.Write([]byte(strings.Join(keys, " ") + "\n"))
	l.mtx.Unlock()
}

func quoteAsNeeded(input string) string {
	if !strings.Contains(input, " ") {
		return input
	}
	return `"` + input + `"`
}

func (l *logger) Level() level.Level {
	return l.level
}

func (l *logger) Close() {
	if l.closer != nil {
		l.closer.Close()
	}
}
