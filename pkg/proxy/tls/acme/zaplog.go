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

package acme

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	logKeyComponent = "component"
	logKeyLogger    = "logger"
	componentACME   = "acme"
)

type zapCore struct {
	fields []zapcore.Field
}

func newZapLogger() *zap.Logger {
	return zap.New(&zapCore{})
}

func tricksterLevel(l zapcore.Level) level.Level {
	switch {
	// the library's info lines trace each step of an order; the manager logs the outcome at info
	case l <= zapcore.InfoLevel:
		return level.Debug
	case l == zapcore.WarnLevel:
		return level.Warn
	default:
		return level.Error
	}
}

func (c *zapCore) Enabled(l zapcore.Level) bool {
	return level.GetID(tricksterLevel(l)) >= level.GetID(logger.Level())
}

func (c *zapCore) With(fields []zapcore.Field) zapcore.Core {
	return &zapCore{fields: append(slices.Clip(c.fields), fields...)}
}

func (c *zapCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c *zapCore) Write(e zapcore.Entry, fields []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}
	pairs := logging.Pairs(enc.Fields)
	pairs[logKeyComponent] = componentACME
	if e.LoggerName != "" {
		pairs[logKeyLogger] = e.LoggerName
	}
	logger.Log(tricksterLevel(e.Level), e.Message, pairs)
	return nil
}

func (c *zapCore) Sync() error {
	return nil
}
