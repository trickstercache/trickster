package main

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

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-stack/stack"
	"gopkg.in/natefinch/lumberjack.v2"
)

// newLogger returns a Logger for the provided logging configuration. The
// returned Logger will write to files distinguished from other Loggers by the
// instance string.
func newLogger(cfg LoggingConfig, instance string) log.Logger {
	var wr io.Writer

	if cfg.LogFile == "" {
		wr = os.Stdout
	} else {
		logFile := cfg.LogFile
		if instance != "" {
			logFile = strings.Replace(logFile, ".log", "."+instance+".log", 1)
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
			return pkgCaller{stack.Caller(5)}
		}),
	)

	// wrap logger depending on log level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		logger = level.NewFilter(logger, level.AllowDebug())
	case "info":
		logger = level.NewFilter(logger, level.AllowInfo())
	case "warn":
		logger = level.NewFilter(logger, level.AllowWarn())
	case "error":
		logger = level.NewFilter(logger, level.AllowError())
	default:
		logger = level.NewFilter(logger, level.AllowInfo())
	}

	return logger
}

// pkgCaller wraps a stack.Call to make the default string output include the
// package path.
type pkgCaller struct {
	c stack.Call
}

func (pc pkgCaller) String() string {
	caller := fmt.Sprintf("%+v", pc.c)
	caller = strings.TrimPrefix(caller, "github.com/comcast/trickster/")
	return caller
}
