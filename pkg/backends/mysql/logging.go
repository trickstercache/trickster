/*
 * Copyright 2026 The Trickster Authors
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

package mysql

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"

	vtlog "vitess.io/vitess/go/vt/log"
)

var installVitessLoggerOnce sync.Once

func installVitessLogger() {
	installVitessLoggerOnce.Do(func() {
		// Vitess's unknown-command diagnostic includes the complete raw command
		// packet. COM_CHANGE_USER and protocol extensions can contain credentials
		// or parameters, so this sanitizes traffic at the time it is logged.
		placeholder := slog.New(slog.NewTextHandler(io.Discard, nil))
		previous := vtlog.SwapLogger(placeholder)
		vtlog.SwapLogger(slog.New(redactingVitessHandler{next: previous.Handler()}))
	})
}

type redactingVitessHandler struct {
	next slog.Handler
}

func (h redactingVitessHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingVitessHandler) Handle(ctx context.Context, record slog.Record) error {
	if strings.Contains(record.Message, "Got unhandled packet (default)") {
		if before, _, ok := strings.Cut(record.Message, "returning error:"); ok {
			record.Message = before + "returning error: [redacted]"
		}
	}
	return h.next.Handle(ctx, record)
}

func (h redactingVitessHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return redactingVitessHandler{next: h.next.WithAttrs(attrs)}
}

func (h redactingVitessHandler) WithGroup(name string) slog.Handler {
	return redactingVitessHandler{next: h.next.WithGroup(name)}
}
