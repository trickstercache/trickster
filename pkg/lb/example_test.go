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
package lb_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

// Balance requests across http.Handlers, giving one of them twice the share.
func Example_httpHandlers() {
	named := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, name) })
	}
	pool, err := lb.NewPool([]*lb.Member{
		lb.NewMember(lb.MemberOptions{Name: "a", Value: named("a")}),
		lb.NewMember(lb.MemberOptions{Name: "b", Weight: 2, Value: named("b")}),
	}, 0)
	if err != nil {
		panic(err)
	}
	defer pool.Stop()
	// rr.New starts its rotation at a random turn; NewAt makes this example's order repeatable
	balancer := lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: pool})

	front := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pick, ok := balancer.Pick(lb.Flow{})
		if !ok {
			http.Error(w, "no backend available", http.StatusBadGateway)
			return
		}
		defer pick.Done(lb.OutcomeOK)
		pick.Member().Value.(http.Handler).ServeHTTP(w, r)
	})

	for range 6 {
		w := httptest.NewRecorder()
		front.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		fmt.Print(w.Body.String(), " ")
	}
	// Output: b b a b b a
}

// upDown is the smallest possible health source: the pool follows it on Refresh
type upDown struct{ status atomic.Int32 }

func (h *upDown) Get() int32 { return h.status.Load() }

// Balance TCP dials across addresses, skipping a member whose health source reports it down.
func Example_tcpDials() {
	serve := func(name string) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = io.WriteString(conn, name)
				_ = conn.Close()
			}
		}()
		return ln.Addr().String()
	}
	health := map[string]*upDown{"one": {}, "two": {}}
	pool, err := lb.NewPool([]*lb.Member{
		lb.NewMember(lb.MemberOptions{Name: "one", Health: health["one"], Value: serve("one")}),
		lb.NewMember(lb.MemberOptions{Name: "two", Health: health["two"], Value: serve("two")}),
	}, 0)
	if err != nil {
		panic(err)
	}
	defer pool.Stop()
	balancer := lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: pool})

	dial := func() string {
		pick, ok := balancer.Pick(lb.Flow{})
		if !ok {
			return "refused"
		}
		start := time.Now()
		conn, err := net.Dial("tcp", pick.Member().Value.(string))
		if err != nil {
			pick.Done(lb.OutcomeConnectFailed)
			return "refused"
		}
		pick.Established(time.Since(start))
		defer pick.Done(lb.OutcomeOK)
		defer conn.Close()
		reply, _ := io.ReadAll(conn)
		return string(reply)
	}

	fmt.Println(dial(), dial(), dial())
	health["two"].status.Store(-1)
	pool.Refresh()
	fmt.Println(dial(), dial())
	// Output:
	// two one two
	// one one
}
