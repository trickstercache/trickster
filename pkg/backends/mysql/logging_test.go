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
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestVitessUnknownCommandLogRedaction(t *testing.T) {
	var output strings.Builder
	handler := redactingVitessHandler{next: slog.NewTextHandler(&output, nil)}
	record := slog.NewRecord(time.Now(), slog.LevelError,
		"Got unhandled packet (default) from client 1, returning error: [17 115 101 99 114 101 116]", 0)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if strings.Contains(got, "115 101 99 114 101 116") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("unsafe Vitess log output: %s", got)
	}
}
