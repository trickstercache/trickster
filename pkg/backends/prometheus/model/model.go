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
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
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
	if httpStatus == 0 {
		httpStatus = 200
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		h.Set(headers.NameContentType, headers.ValueApplicationJSON+"; charset=UTF-8")
		rw.WriteHeader(httpStatus)
	}
	w.Write([]byte(fmt.Sprintf(`{"status":"%s"`, e.Status)))

	if e.Error != "" {
		w.Write([]byte(fmt.Sprintf(`,"error":"%s"`, e.Error)))
	}

	if e.ErrorType != "" {
		w.Write([]byte(fmt.Sprintf(`,"errorType":"%s"`, e.ErrorType)))
	}

	if len(e.Warnings) > 0 {
		w.Write([]byte(fmt.Sprintf(`,"warnings":["%s"]`, strings.Join(e.Warnings, `","`))))

	}
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
	if e.Status == "error" && e2.Status == "success" {
		e.Status = "success"
		e.Warnings = append(e.Warnings, e.Error)
		e.Error = ""
	}

}
