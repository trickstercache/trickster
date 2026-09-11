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

package errors

import (
	"errors"
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrBadRequest, "bad request"},
		{ErrNilWriter, "nil writer"},
		{ErrInvalidOptions, "invalid options"},
		{ErrServerAlreadyStarted, "server is already started"},
		{ErrMissingPathConfig, "missing path config"},
		{ErrInvalidPath, "invalid path value in config"},
		{ErrInvalidMethod, "invalid method value in config"},
		{ErrNoValidBackends, "no valid backends configured"},
		{ErrInvalidListenPort, "invalid listen port in config"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestNewErrorBody(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil",
			err:  nil,
			want: "{}",
		},
		{
			name: "sentinel",
			err:  ErrBadRequest,
			want: `{"error":"bad request"}`,
		},
		{
			name: "custom",
			err:  errors.New("something went wrong"),
			want: `{"error":"something went wrong"}`,
		},
		{
			name: "needs json escape",
			err:  errors.New(`quote " and \ backslash`),
			want: `{"error":"quote \" and \\ backslash"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewErrorBody(tt.err); got != tt.want {
				t.Errorf("NewErrorBody() = %q, want %q", got, tt.want)
			}
		})
	}
}
