package logger

import (
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
)

var logger logging.Logger = logging.ConsoleLogger(level.Info)

func Logger() logging.Logger {
	return logger
}

// SetLogger sets the package-level logger object
func SetLogger(l logging.Logger) {
	if l != nil {
		fmt.Println("SET LOGGER")
		logger = l
	}
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
