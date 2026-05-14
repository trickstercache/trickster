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

package timeseries

import "io"

// FormatHintReader wraps an io.Reader with a format hint from the upstream
// response (e.g., the X-ClickHouse-Format header). Provider modelers can
// type-assert on this to detect the wire format without byte-peeking.
type FormatHintReader struct {
	io.Reader
	Format string
}

// NewFormatHintReader wraps a reader with a format hint.
func NewFormatHintReader(r io.Reader, format string) *FormatHintReader {
	return &FormatHintReader{Reader: r, Format: format}
}
