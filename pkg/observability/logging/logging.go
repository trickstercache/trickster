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
	"cmp"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	tstr "github.com/trickstercache/trickster/v2/pkg/util/strings"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

var (
	_ Logger    = &logger{}
	_ io.Writer = &logger{}
)

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
		now: time.Now,
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
		logFunc: func(level.Level, string, Pairs) {},
		levelID: level.InfoID,
		level:   level.Info,
		now:     time.Now,
	}
	return l
}

func StreamLogger(w io.Writer, logLevel level.Level) Logger {
	l := &logger{
		writer: w,
		now:    time.Now,
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
		writer: os.Stdout,
		now:    time.Now,
	}
	l.logFunc = l.logAsyncronous
	l.SetLogLevel(logLevel)
	return l
}

type logger struct {
	level          level.Level
	levelID        level.ID
	writer         io.Writer
	closer         io.Closer
	mtx            sync.Mutex
	onceRanEntries sync.Map
	logFunc        logFunc
	now            func() time.Time
}

func (l *logger) Write(b []byte) (int, error) {
	if l.writer == nil {
		return 0, nil
	}
	return l.writer.Write(b)
}

func (l *logger) SetLogLevel(logLevel level.Level) {
	id := level.GetID(logLevel)
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
	lid := level.GetID(logLevel)
	if lid == 0 || lid < l.levelID {
		return
	}
	l.logFunc(logLevel, event, detail)
}

func (l *logger) logFuncConditionally(level level.Level, levelID level.ID, event string, detail Pairs) {
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
	lid := level.GetID(logLevel)
	if lid == 0 || lid < l.levelID {
		return
	}
	l.log(logLevel, event, detail)
}

func (l *logger) logConditionally(level level.Level, levelID level.ID, event string, detail Pairs) {
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
	lid := level.GetID(logLevel)
	return l.logOnce(logLevel, lid, key, event, detail)
}

func (l *logger) logOnce(logLevel level.Level, lid level.ID,
	key, event string, detail Pairs,
) bool {
	if lid == 0 || lid < l.levelID || l.HasLoggedOnce(logLevel, key) {
		return false
	}
	key = string(logLevel) + "." + key
	_, ok := l.onceRanEntries.Load(key)
	if !ok {
		// load or store is more expensive than load, so check via load first
		// and use LoadOrStore to ensure that log is only called once
		_, ok = l.onceRanEntries.LoadOrStore(key, true)
		if !ok {
			l.log(logLevel, event, detail)
		}
	}
	return !ok
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
	_, ok := l.onceRanEntries.Load(key)
	return ok
}

func (l *logger) logAsyncronous(logLevel level.Level, event string, detail Pairs) {
	go l.logWithStack(logLevel, event, detail, getCallerStack(1))
}

type item struct {
	key string
	val string
}

func (i *item) Bytes() []byte {
	return append([]byte(i.key), append([]byte(equal), []byte(i.val)...)...)
}

const (
	space   = " "
	equal   = "="
	newline = "\n"
)

// getCallerStack returns the first path in the call stack from /pkg not in
func getCallerStack(skip int) string {
	for s := skip; s < skip+20; s++ {
		pc, file, line, ok := runtime.Caller(s)
		if !ok {
			break
		}
		idx := strings.Index(file, "/pkg/")
		if idx == -1 || strings.Contains(file, "/pkg/observability/logging/") {
			continue
		}
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}
		return file[idx+1:] + ":" + strconv.Itoa(line)
	}
	return ""
}

func (l *logger) log(logLevel level.Level, event string, detail Pairs) {
	// For synchronous logging, capture caller here
	// Call stack from getCallerStack's perspective:
	// runtime.Caller(1) = log
	// runtime.Caller(2) = logConditionally or logFuncConditionally
	// runtime.Caller(3) = Warn/Info/etc in logging package
	// runtime.Caller(4) = Warn/Info/etc in logger package
	// runtime.Caller(5) = actual caller
	// Start from skip 1 to check log itself, then walk up
	stack := getCallerStack(1)
	l.logWithStack(logLevel, event, detail, stack)
}

func (l *logger) logWithStack(logLevel level.Level, event string, detail Pairs, stack string) {
	if l.writer == nil {
		return
	}
	ts := l.now()
	ld := len(detail)
	if strings.HasPrefix(event, space) || strings.HasSuffix(event, space) {
		event = strings.TrimSpace(event)
	}

	logLine := []byte(
		"time=" + ts.UTC().Format(time.RFC3339Nano) + space +
			"app=trickster" + space +
			"level=" + string(logLevel) + space +
			"event=" + quoteAsNeeded(event),
	)

	// Add stack field if available
	if stack != "" {
		logLine = append(logLine, []byte(space+"stack="+stack)...)
	}

	if ld > 0 {
		logLine = append(logLine, []byte(space)...)
		keyPairs := make([]item, ld)
		var i int
		for k, v := range detail {
			var s string
			var ok bool
			if s, ok = v.(string); ok {
				s = quoteAsNeeded(s)
			} else if stringer, ok := v.(fmt.Stringer); ok {
				s = quoteAsNeeded(stringer.String())
			} else if err, ok := v.(error); ok {
				s = quoteAsNeeded(err.Error())
			} else {
				s = fmt.Sprintf("%v", v)
			}
			keyPairs[i] = item{k, s}
			i++
		}
		slices.SortFunc(keyPairs, func(a, b item) int {
			return cmp.Compare(a.key, b.key)
		})
		i = 0
		for _, v := range keyPairs {
			logLine = append(logLine, v.Bytes()...)
			i++
			if i < ld {
				logLine = append(logLine, []byte(space)...)
			}
		}
	}
	l.mtx.Lock()
	l.writer.Write(append(logLine, []byte(newline)...))
	l.mtx.Unlock()
}

func quoteAsNeeded(input string) string {
	if !strings.Contains(input, " ") {
		return input
	}
	return `"` + tstr.EscapeQuotes(input) + `"`
}

func (l *logger) Level() level.Level {
	return l.level
}

func (l *logger) Close() {
	if l.closer != nil {
		l.closer.Close()
	}
}
