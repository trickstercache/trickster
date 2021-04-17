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

package reader

import (
	"bytes"
	"io"
	"testing"
)

func TestReadCloserResetter(t *testing.T) {
	r := io.NopCloser(bytes.NewReader([]byte("trickster")))
	rcr := NewReadCloserResetter(r)
	if rcr == nil {
		t.Error("expected non-nil")
	}
	rcr2 := NewReadCloserResetter(rcr)
	if rcr2 == nil {
		t.Error("expected non-nil")
	}

	err := rcr.Close()
	if err != nil {
		t.Error(err)
	}
	// catches atomic > 1 case
	err = rcr.Close()
	if err != nil {
		t.Error(err)
	}

	err = rcr.Reset(r)
	if err != nil {
		t.Error(err)
	}

	err = rcr2.Reset(r)
	if err != nil {
		t.Error(err)
	}

}

func TestReadCloserResetterBytes(t *testing.T) {
	r := NewReadCloserResetterBytes([]byte("trickster"))
	if r == nil {
		t.Error("expected non-nil")
	}
	err := r.Close()
	if err != nil {
		t.Error(err)
	}
}

func TestBaseReader(t *testing.T) {
	r := bytes.NewReader([]byte("trickster"))
	rcr := &readCloserResetter{Reader: r}
	if rcr.BaseReader() == nil {
		t.Error("expected non-nil")
	}
}
