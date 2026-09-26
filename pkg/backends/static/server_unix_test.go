//go:build unix

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

package static

import (
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestServeRefusesPipe(t *testing.T) {
	root := newTestSite(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Skipf("unable to create a named pipe: %v", err)
	}
	s := newTestServer(t, testOptions(root))
	done := make(chan int, 1)
	go func() { done <- get(t, s, http.MethodGet, "/pipe").StatusCode }()
	select {
	case status := <-done:
		if status != http.StatusNotFound {
			t.Errorf("expected a named pipe to look absent, got %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request for a named pipe blocked")
	}
}
