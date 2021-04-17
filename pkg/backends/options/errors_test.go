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

package options

import (
	"errors"
	"testing"
)

func TestErrMissingProvider(t *testing.T) {
	err := NewErrMissingProvider("test")
	var e *ErrMissingProvider
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}

}

func TestErrInvalidALBOptions(t *testing.T) {
	err := NewErrInvalidALBOptions("test", "test2")
	var e *ErrInvalidALBOptions
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}

func TestNewErrMissingOriginURL(t *testing.T) {
	err := NewErrMissingOriginURL("test")
	var e *ErrMissingOriginURL
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}

}

func TestInvalidNegativeCacheName(t *testing.T) {
	err := NewErrInvalidNegativeCacheName("test")
	var e *ErrInvalidNegativeCacheName
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}

func TestInvalidRuleName(t *testing.T) {
	err := NewErrInvalidRuleName("testRule", "testBackend")
	var e *ErrInvalidRuleName
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}

}

func TestInvalidCacheName(t *testing.T) {
	err := NewErrInvalidCacheName("testCache", "testBackend")
	var e *ErrInvalidCacheName
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}

func TestInvalidBackendName(t *testing.T) {
	err := NewErrInvalidBackendName("testBackend")
	var e *ErrInvalidBackendName
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}

func TestInvalidRewriterName(t *testing.T) {
	err := NewErrInvalidRewriterName("testRewriter", "testBackend")
	var e *ErrInvalidRewriterName
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}
