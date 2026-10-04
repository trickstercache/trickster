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

package model

import (
	"io"
	"net/http"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type ResultType string

const (
	Scalar ResultType = "scalar"
	Vector ResultType = "vector"
	Matrix ResultType = "matrix"
)

const (
	statusSuccess = "success"
	statusErr     = "error"
)

// Envelope represents a Proemtheus Response Envelope Root Type
type Envelope struct {
	Status    string   `json:"status"`
	ErrorType string   `json:"errorType,omitempty"`
	Error     string   `json:"error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// StartMarshal writes the opening envelope data to the wire;
// the caller must Close the envelope by writing "}" after writing
// the data block
func (e *Envelope) StartMarshal(w io.Writer, httpStatus int) {
	if w == nil {
		return
	}
	startResponse(w, httpStatus)
	jw := tbytes.NewChunkWriter(w)
	jw.Buf = e.appendStart(jw.Buf)
	// the envelope's opening has no caller to report a failed write to, as before
	_ = jw.Close()
}

// sets the status and Content-Type of a response writer, which must precede its body
func startResponse(w io.Writer, httpStatus int) {
	if httpStatus == 0 {
		httpStatus = http.StatusOK
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		h.Set(headers.NameContentType, headers.ValueApplicationJSON+"; charset=UTF-8")
		rw.WriteHeader(httpStatus)
	}
}

func (e *Envelope) appendStart(dst []byte) []byte {
	dst = append(dst, `{"status":`...)
	dst = tstrings.AppendJSON(dst, e.Status)
	if e.Error != "" {
		dst = append(dst, `,"error":`...)
		dst = tstrings.AppendJSON(dst, e.Error)
	}
	if e.ErrorType != "" {
		dst = append(dst, `,"errorType":`...)
		dst = tstrings.AppendJSON(dst, e.ErrorType)
	}
	if len(e.Warnings) > 0 {
		dst = append(dst, `,"warnings":[`...)
		for i, w := range e.Warnings {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = tstrings.AppendJSON(dst, w)
		}
		dst = append(dst, ']')
	}
	return dst
}

// Merge combines the passed envelope data with the subject data
func (e *Envelope) Merge(e2 *Envelope) {
	if e2.Error != "" {
		e.Warnings = append(e.Warnings, e2.Error)
	}
	if len(e2.Warnings) > 0 {
		e.Warnings = append(e.Warnings, e2.Warnings...)
	}

	// if one of the two statuses is success, the resulting status should be
	// the warnings will pick up any errors from the merged envelope
	if e.Status == statusErr && e2.Status == statusSuccess {
		e.Status = statusSuccess
		e.Warnings = append(e.Warnings, e.Error)
		e.Error = ""
	}
}
