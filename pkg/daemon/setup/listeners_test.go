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

package setup

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func TestListenerEnabledOn(t *testing.T) {
	tests := []struct {
		configuredListener string
		listenerName       string
		want               bool
	}{
		{mgmt.ListenerNameMgmt, mgmt.ListenerNameMgmt, true},
		{mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics, false},
		{mgmt.ListenerNameMetrics, mgmt.ListenerNameMgmt, false},
		{mgmt.ListenerNameMetrics, mgmt.ListenerNameMetrics, true},
		{mgmt.ListenerNameBoth, mgmt.ListenerNameMgmt, true},
		{mgmt.ListenerNameBoth, mgmt.ListenerNameMetrics, true},
		{mgmt.ListenerNameOff, mgmt.ListenerNameMgmt, false},
		{mgmt.ListenerNameOff, mgmt.ListenerNameMetrics, false},
	}

	for _, test := range tests {
		if got := listenerEnabledOn(test.configuredListener, test.listenerName); got != test.want {
			t.Errorf("listenerEnabledOn(%q, %q) = %t, want %t",
				test.configuredListener, test.listenerName, got, test.want)
		}
	}
}

func TestDesiredListeners(t *testing.T) {
	c := config.NewConfig()
	c.Listeners["custom"] = listenerconfig.New("custom")
	c.Listeners["custom"].ListenPort = 9000
	c.Listeners["custom"].Active = true
	routers := map[string]router.Router{
		listenerconfig.DefaultFrontendName: lm.NewRouter(),
		"custom":                           lm.NewRouter(),
	}

	got := desiredListeners(c, routers, lm.NewRouter(), lm.NewRouter())
	for _, key := range []string{
		listenerKey(listenerconfig.DefaultFrontendName, false),
		listenerKey(mgmt.ListenerNameMetrics, false),
		listenerKey(mgmt.ListenerNameMgmt, false),
		listenerKey("custom", false),
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing desired listener %q", key)
		}
	}
	if _, ok := got[listenerKey(listenerconfig.DefaultFrontendName, true)]; ok {
		t.Errorf("TLS listener should not be desired until ServeTLS is enabled")
	}
}

func TestListenerNeedsRestart(t *testing.T) {
	o := listenerconfig.New("custom")
	o.ListenPort = 9000
	old := desiredListener{address: "127.0.0.1", port: 9000, options: o}
	current := old
	current.router = lm.NewRouter()
	if listenerNeedsRestart(old, current) {
		t.Errorf("router-only update should not restart a listener")
	}
	current.port = 9001
	if !listenerNeedsRestart(old, current) {
		t.Errorf("port change should restart a listener")
	}
}

func TestApplyListenerConfigsReloadReconciliation(t *testing.T) {
	firstPort := availablePort(t)
	secondPort := availablePort(t)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })

	conf := config.NewConfig()
	for _, name := range []string{listenerconfig.DefaultFrontendName, mgmt.ListenerNameMgmt, mgmt.ListenerNameMetrics} {
		conf.Listeners[name].ListenPort = 0
		conf.Listeners[name].TLSListenPort = 0
		conf.Listeners[name].Active = false
	}
	conf.Listeners["custom"] = listenerconfig.New("custom")
	conf.Listeners["custom"].ListenAddress = "127.0.0.1"
	conf.Listeners["custom"].ListenPort = firstPort
	conf.Listeners["custom"].Active = true

	firstRouter := markerRouter("first")
	applyListenerConfigs(conf, nil, map[string]router.Router{"custom": firstRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	key := listenerKey("custom", false)
	waitForListener(t, group, key)
	original := group.Get(key)
	assertResponseBody(t, firstPort, "first")

	// A router-only reload must retain the socket and atomically swap handlers.
	secondConf := conf.Clone()
	secondRouter := markerRouter("second")
	applyListenerConfigs(secondConf, conf, map[string]router.Router{"custom": secondRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	if group.Get(key) != original {
		t.Errorf("unchanged listener socket was restarted")
	}
	assertResponseBody(t, firstPort, "second")

	// A port change drains the old socket and starts the replacement.
	thirdConf := secondConf.Clone()
	thirdConf.Listeners["custom"].ListenPort = secondPort
	applyListenerConfigs(thirdConf, secondConf, map[string]router.Router{"custom": secondRouter},
		http.NotFoundHandler(), lm.NewRouter(), nil, nil, nil, group)
	waitForListener(t, group, key)
	if group.Get(key) == original {
		t.Errorf("changed listener port did not restart the socket")
	}
	assertResponseBody(t, secondPort, "second")
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", firstPort)); err == nil {
		response.Body.Close()
		t.Errorf("old listener port is still accepting requests")
	}
}

func markerRouter(marker string) router.Router {
	r := lm.NewRouter()
	r.RegisterRoute("/", nil, nil, true, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(marker))
	}))
	return r
}

func availablePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForListener(t *testing.T, group *listener.Group, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l := group.Get(key); l != nil && l.State() == listener.StateReady {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener %q did not become ready", key)
}

func assertResponseBody(t *testing.T, port int, want string) {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != want {
		t.Errorf("response body = %q, want %q", body, want)
	}
}
