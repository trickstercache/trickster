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

package pick

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

const eventWait = 2 * time.Second

// sseMember sends one event, flushes it, and sends the next only once the client has read the
// first, so a flush that never reaches the network stalls the stream
func sseMember(flush func(http.ResponseWriter), read <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: 1\n\n"))
		flush(w)
		select {
		case <-read:
		case <-time.After(2 * eventWait):
		}
		_, _ = w.Write([]byte("data: 2\n\n"))
	})
}

// requireEventsStream reads the stream's first event within a deadline, while the member is
// still holding the second one back
func requireEventsStream(t *testing.T, alb http.Handler, read chan<- struct{}) {
	t.Helper()
	// the access log wraps every writer an ALB is handed, and it only unwraps
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alb.ServeHTTP(middleware.NewResponseObserver(w), r)
	}))
	defer srv.Close()
	// the headers travel with the first flush too, so the request itself is under the deadline
	lines := make(chan string, 1)
	go func() {
		resp, err := http.Get(srv.URL)
		if err != nil {
			lines <- err.Error()
			return
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		lines <- line
	}()
	defer close(read)
	select {
	case line := <-lines:
		if line != "data: 1\n" {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(eventWait):
		t.Fatal("the first event was not delivered before the next was written")
	}
}

func TestTimedDispatchDeliversEachFlush(t *testing.T) {
	flushes := map[string]func(http.ResponseWriter){
		"flusher": func(w http.ResponseWriter) { w.(http.Flusher).Flush() },
		"controller": func(w http.ResponseWriter) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				panic(err)
			}
		},
	}
	for name, flush := range flushes {
		t.Run(name, func(t *testing.T) {
			read := make(chan struct{})
			h, _ := newLT(t, sseMember(flush, read))
			requireEventsStream(t, h, read)
		})
	}
}

// a sticky ALB sends every request through the writer that issues its token, so it must flush
// as the timed one does, and the token must travel with the headers the first flush sends
func TestStickyDispatchDeliversEachFlush(t *testing.T) {
	read := make(chan struct{})
	flush := func(w http.ResponseWriter) { w.(http.Flusher).Flush() }
	h := stickyALB(t, "{}", rr.New(), named(t, "sse", sseMember(flush, read)))
	requireEventsStream(t, h, read)
	// a member that flushes before it writes sends the headers then, with the token among them
	early := stickyALB(t, "{}", rr.New(), named(t, "early", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flush(w)
		_, _ = w.Write([]byte("data: 1\n\n"))
	})))
	w := httptest.NewRecorder()
	early.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	if !w.Flushed || w.Result().Header.Get("Set-Cookie") == "" {
		t.Error("the headers a flush sent carried no token")
	}
}

// a flush sends the headers when nothing was written yet, so it is the member's first write
func TestFlushIsTheFirstWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	fw := getWriter(rec, lb.Pick{})
	defer putWriter(fw)
	if err := fw.FlushError(); err != nil || !fw.wrote || fw.code != http.StatusOK || !rec.Flushed {
		t.Errorf("flush: %v, wrote %v, code %d, flushed %v", err, fw.wrote, fw.code, rec.Flushed)
	}
	// a stream flushes per event, so the walk down the writers must not allocate
	if n := testing.AllocsPerRun(100, fw.Flush); n != 0 {
		t.Errorf("a flush allocates %v times", n)
	}
	var plain plainWriter
	pw := getWriter(&plain, lb.Pick{})
	defer putWriter(pw)
	if err := pw.FlushError(); err == nil {
		t.Error("a writer that cannot flush reported a flush")
	}
}
