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

package certificates

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestHandlerFunc(t *testing.T) {
	k, c, err := tlstest.GetTestKeyAndCertWithNames("inventory.example.com")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	lg := listener.NewGroup()
	key := listener.GroupKey("default", "", true)
	go lg.StartListener(key, "127.0.0.1", 0, 0,
		&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
		nil, nil, time.Second, time.Second)
	t.Cleanup(func() { lg.Shutdown(0) })
	deadline := time.Now().Add(5 * time.Second)
	for lg.Get(key) == nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if l := lg.Get(key); l == nil || !l.WaitForReady(5*time.Second) {
		t.Fatal("listener not ready")
	}

	w := httptest.NewRecorder()
	HandlerFunc(lg)(w, httptest.NewRequest(http.MethodGet, "/trickster/certificates", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "PRIVATE KEY") {
		t.Fatal("inventory must never contain key material")
	}
	out := struct {
		Listeners map[string][]tr.EntryInfo `json:"listeners"`
	}{}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	entries, ok := out.Listeners[key]
	if !ok || len(entries) != 1 {
		t.Fatalf("expected 1 entry for %s, got %+v", key, out.Listeners)
	}
	if entries[0].CommonName != "inventory.example.com" ||
		entries[0].SourceKind != tr.SourceKindConfig || entries[0].NotAfter.IsZero() {
		t.Errorf("unexpected inventory entry: %+v", entries[0])
	}

	// a nil group yields an empty inventory, not an error
	w = httptest.NewRecorder()
	HandlerFunc(nil)(w, httptest.NewRequest(http.MethodGet, "/trickster/certificates", nil))
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for nil group, got %d", w.Code)
	}
}
