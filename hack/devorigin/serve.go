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

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks"
)

const textFormat = "text/plain; version=0.0.4; charset=utf-8"

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second // covers slow request bodies
	writeTimeout      = 2 * time.Minute  // leaves room for a full-size (1 GiB) rangesim response
	idleTimeout       = time.Minute
	maxHeaderBytes    = 64 << 10
	maxBodyBytes      = 1 << 20
)

type exporter struct {
	mu  sync.Mutex
	acc *accumulator
	now func() time.Time
	buf []byte
}

func (e *exporter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// trips are counted as scrapes arrive, so values always match the scrape time
	if err := e.acc.advance(e.now().Unix()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	e.buf = e.acc.appendText(e.buf[:0])
	w.Header().Set("Content-Type", textFormat)
	w.Write(e.buf)
}

func newMux(e *exporter) *http.ServeMux {
	mux := mocks.NewRouter()
	mux.Handle("/metrics", e)
	return mux
}

func newServer(addr string, e *exporter) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(newMux(e), maxBodyBytes),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

func serve(dataDir, addr string, log io.Writer) error {
	src, err := openTrips(dataDir)
	if err != nil {
		return err
	}
	defer src.Close()
	e := &exporter{acc: newAccumulator(src), now: time.Now}
	began := time.Now()
	if err := e.acc.advance(began.Unix()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "caught up to %s in %s; serving /metrics, /prometheus/ and /byterange/ on %s\n",
		began.UTC().Format(time.RFC3339), time.Since(began).Round(time.Millisecond), addr)

	srv := newServer(addr, e)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
