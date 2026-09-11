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
	"strings"
	"testing"
)

func TestNewErrInvalidALBOptions(t *testing.T) {
	t.Parallel()
	err := NewErrInvalidALBOptions("backend1")
	if _, ok := errors.AsType[*InvalidALBOptionsError](err); !ok {
		t.Fatalf("expected *InvalidALBOptionsError, got %T", err)
	}
	if !strings.Contains(err.Error(), "backend1") {
		t.Errorf("error %q missing backend name", err.Error())
	}
}

func TestNewErrInvalidPoolMemberName(t *testing.T) {
	t.Parallel()
	err := NewErrInvalidPoolMemberName("alb1", "missing")
	if _, ok := errors.AsType[*InvalidALBOptionsError](err); !ok {
		t.Fatalf("expected *InvalidALBOptionsError, got %T", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "alb1") || !strings.Contains(msg, "missing") {
		t.Errorf("error %q missing names", msg)
	}
}

func TestNewErrInvalidBackendName(t *testing.T) {
	t.Parallel()
	err := NewErrInvalidBackendName("alb1", "bad")
	if _, ok := errors.AsType[*InvalidALBOptionsError](err); !ok {
		t.Fatalf("expected *InvalidALBOptionsError, got %T", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "alb1") || !strings.Contains(msg, "bad") {
		t.Errorf("error %q missing names", msg)
	}
}

func TestNewErrInvalidUserRouterCreds(t *testing.T) {
	t.Parallel()
	err := NewErrInvalidUserRouterCreds("alb1")
	if _, ok := errors.AsType[*InvalidALBOptionsError](err); !ok {
		t.Fatalf("expected *InvalidALBOptionsError, got %T", err)
	}
	if !strings.Contains(err.Error(), "alb1") {
		t.Errorf("error %q missing alb name", err.Error())
	}
	if !strings.Contains(err.Error(), "authenticator_name") {
		t.Errorf("error %q missing authenticator hint", err.Error())
	}
}

func TestSentinelErrors(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		ErrInvalidTimeSeriesMergeProvider,
		ErrUnsupportedMechanism,
		ErrInvalidOptionsMetadata,
	} {
		if err == nil || err.Error() == "" {
			t.Errorf("expected non-empty sentinel error")
		}
	}
}
