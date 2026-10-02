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

package mysql

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"

	vtmysql "vitess.io/vitess/go/mysql"
)

// balancedTestConfig is the routed test config with its user router replaced by a strategy
// that balances the listener's sessions over both targets
func balancedTestConfig(mechanism string) *config.Config {
	c := routedRestartTestConfig()
	lb := c.Backends["mysql-users"]
	lb.ALBOptions = ao.New()
	lb.ALBOptions.MechanismName = mechanism
	lb.ALBOptions.Pool = ao.Members("mysql-b", "mysql-a", "mysql-b")
	return c
}

func TestNativeBalancerIsRecognized(t *testing.T) {
	c := balancedTestConfig("rr")
	lb := c.Backends["mysql-users"]
	if !isNativeBalancer(c, lb) || !isNativeRouter(c, lb) || isNativeUserRouter(lb) {
		t.Fatal("a round robin pool of mysql backends is not a session balancer")
	}
	if got := routeTargetNames(lb); !slices.Equal(got, []string{"mysql-a", "mysql-b"}) {
		t.Fatalf("route targets = %v", got)
	}
	a := nativeListenerAdapter{}
	if err := a.ValidateBalancer(c, "mysql-users", lb); err != nil {
		t.Fatal(err)
	}
	for name, breakIt := range map[string]func(*config.Config){
		"nil config":          nil,
		"fanout mechanism":    func(c *config.Config) { c.Backends["mysql-users"].ALBOptions.MechanismName = "fr" },
		"latency mechanism":   func(c *config.Config) { c.Backends["mysql-users"].ALBOptions.MechanismName = "lt" },
		"router mechanism":    func(c *config.Config) { c.Backends["mysql-users"].ALBOptions.MechanismName = "ur" },
		"empty pool":          func(c *config.Config) { c.Backends["mysql-users"].ALBOptions.Pool = nil },
		"missing member":      func(c *config.Config) { delete(c.Backends, "mysql-b") },
		"foreign member":      func(c *config.Config) { c.Backends["mysql-b"].Provider = providers.ReverseProxyShort },
		"not a load balancer": func(c *config.Config) { c.Backends["mysql-users"].Provider = providers.MySQL },
	} {
		broken := balancedTestConfig("rr")
		if breakIt == nil {
			if isNativeBalancer(nil, broken.Backends["mysql-users"]) {
				t.Errorf("%s: recognized", name)
			}
			continue
		}
		breakIt(broken)
		if isNativeBalancer(broken, broken.Backends["mysql-users"]) {
			t.Errorf("%s: recognized as a session balancer", name)
		}
		if err := a.ValidateBalancer(broken, "mysql-users", broken.Backends["mysql-users"]); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	// the listener authenticates its own clients, so the load balancer must name them
	anonymous := balancedTestConfig("rr")
	anonymous.Backends["mysql-users"].AuthOptions = nil
	err := a.ValidateBalancer(anonymous, "mysql-users", anonymous.Backends["mysql-users"])
	if err == nil || !strings.Contains(err.Error(), "authenticator_name") {
		t.Fatalf("a load balancer with no listener-facing users: %v", err)
	}
}

func TestNativeBalancerBuildsARoutedServer(t *testing.T) {
	a := nativeListenerAdapter{}
	c := balancedTestConfig("lc")
	protocolConfig, routed, err := nativeProtocolConfig(c, "mysql-users")
	if err != nil || !routed || protocolConfig.DownstreamUsers["alice"] == "" {
		t.Fatalf("nativeProtocolConfig = %+v, %v, %v", protocolConfig, routed, err)
	}
	first, err := a.Describe(c, "mysql-users")
	if err != nil {
		t.Fatal(err)
	}
	// the strategy and its weights are swapped on reload; the set of targets is not
	reweighted := balancedTestConfig("p2c")
	reweighted.Backends["mysql-users"].ALBOptions.Pool[1].Weight = 5
	if again, _ := a.Describe(reweighted, "mysql-users"); again.RestartKey != first.RestartKey {
		t.Error("a change of strategy or weight restarts the listener")
	}
	smaller := balancedTestConfig("lc")
	smaller.Backends["mysql-users"].ALBOptions.Pool = ao.Members("mysql-a")
	if again, _ := a.Describe(smaller, "mysql-users"); again.RestartKey == first.RestartKey {
		t.Error("a change of targets does not restart the listener")
	}

	request := nativeTestBuildRequest(c, "mysql-users", nil)
	request.BackendClients = backends.Backends{
		"mysql-users": &nativeRuntimeBackend{resolver: staticRouteResolver{}},
		"mysql-a":     &nativeRuntimeBackend{protocolConfig: ProtocolConfig{BackendName: "mysql-a"}},
		"mysql-b":     &nativeRuntimeBackend{protocolConfig: ProtocolConfig{BackendName: "mysql-b"}},
	}
	resolver, targets := nativeRouteRuntime(request)
	if resolver == nil || len(targets) != 2 {
		t.Fatalf("nativeRouteRuntime = %v, %v", resolver, targets)
	}
	server, err := a.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type countedResolver struct {
	target      backends.Backend
	unavailable bool
	input       backends.RouteInput
	released    int
}

type failingStatus struct{}

func (failingStatus) Get() int32 { return -1 }

func (r *countedResolver) ResolveRoute(in backends.RouteInput) (backends.RouteDecision, bool) {
	r.input = in
	d := backends.RouteDecision{
		Target: backends.RouteTarget{Backend: r.target}, Release: func() { r.released++ },
	}
	if r.unavailable {
		d.Target.Status = failingStatus{}
	}
	return d, true
}

func TestRoutedSessionsAreReleasedOnce(t *testing.T) {
	salt := []byte("12345678901234567890")
	response := vtmysql.ScrambleMysqlNativePassword(salt, []byte("password"))
	users := map[string]string{"client": "password"}
	peer := &net.TCPAddr{IP: net.ParseIP("::ffff:198.51.100.7"), Port: 40000}

	t.Run("closed after activation", func(t *testing.T) {
		routed, _ := newRoutedHandlerForTest(t)
		r := &countedResolver{target: newRouteBackend(t, "mysql-a")}
		c := &vtmysql.Conn{ConnectionID: 21}
		routed.setControl(c.ConnectionID, newTestControl(t))
		routed.NewConnection(c)
		if _, err := newCredentialAuth(users, "lb", r).UserEntryWithHash(c, salt, "client", response, peer); err != nil {
			t.Fatal(err)
		}
		if r.input.Client != netip.MustParseAddr("198.51.100.7") || r.input.Username != "client" {
			t.Errorf("route input = %+v", r.input)
		}
		if _, err := routed.activate(c); err != nil {
			t.Fatal(err)
		}
		if r.released != 0 {
			t.Fatal("an open session was released")
		}
		routed.ConnectionClosed(c)
		if r.released != 1 {
			t.Fatalf("released %d times", r.released)
		}
	})
	t.Run("closed before its first command", func(t *testing.T) {
		routed, _ := newRoutedHandlerForTest(t)
		r := &countedResolver{target: newRouteBackend(t, "mysql-a")}
		c := &vtmysql.Conn{ConnectionID: 22}
		routed.NewConnection(c)
		if _, err := newCredentialAuth(users, "lb", r).UserEntryWithHash(c, salt, "client", response, nil); err != nil {
			t.Fatal(err)
		}
		routed.ConnectionClosed(c)
		if r.released != 1 {
			t.Fatalf("released %d times", r.released)
		}
	})
	t.Run("target unknown to the listener", func(t *testing.T) {
		routed, _ := newRoutedHandlerForTest(t)
		r := &countedResolver{target: newRouteBackend(t, "mysql-missing")}
		c := &vtmysql.Conn{ConnectionID: 23}
		routed.NewConnection(c)
		if _, err := newCredentialAuth(users, "lb", r).UserEntryWithHash(c, salt, "client", response, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := routed.activate(c); err == nil {
			t.Fatal("activated an unknown target")
		}
		routed.ConnectionClosed(c)
		if r.released != 1 {
			t.Fatalf("released %d times", r.released)
		}
	})
	t.Run("target unavailable", func(t *testing.T) {
		r := &countedResolver{target: newRouteBackend(t, "mysql-a"), unavailable: true}
		if _, err := newCredentialAuth(users, "lb", r).UserEntryWithHash(&vtmysql.Conn{}, salt, "client", response, nil); err == nil {
			t.Fatal("authenticated onto an unavailable target")
		}
		if r.released != 1 {
			t.Fatalf("released %d times", r.released)
		}
	})
}

type namedAddr string

func (namedAddr) Network() string { return "test" }

func (a namedAddr) String() string { return string(a) }

func TestClientAddr(t *testing.T) {
	for want, remote := range map[string]net.Addr{
		"198.51.100.7": &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 1},
		"2001:db8::1":  namedAddr("[2001:db8::1]:3306"),
		"203.0.113.4":  namedAddr("[::ffff:203.0.113.4]:3306"),
	} {
		if got := clientAddr(remote); got != netip.MustParseAddr(want) {
			t.Errorf("clientAddr(%v) = %v, want %s", remote, got, want)
		}
	}
	if clientAddr(nil).IsValid() || clientAddr(namedAddr("pipe")).IsValid() {
		t.Error("an address that is not an IP's produced one")
	}
}
