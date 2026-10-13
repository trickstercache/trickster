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

package alb

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/stretchr/testify/require"
)

// retiringMechanism is a mechanism keeping the context of each retirement
type retiringMechanism struct {
	http.Handler
	retired []context.Context
}

func (*retiringMechanism) Name() types.Name { return "retiring" }

func (m *retiringMechanism) Retire(ctx context.Context) { m.retired = append(m.retired, ctx) }

type plainMechanism struct{ http.Handler }

func (plainMechanism) Name() types.Name { return "plain" }

func TestClientRetireRetiresItsMechanism(t *testing.T) {
	m := &retiringMechanism{Handler: http.NotFoundHandler()}
	var c backends.Backend = &Client{handler: m}
	ctx := context.Background()
	backends.Retire(ctx, c)
	require.Equal(t, []context.Context{ctx}, m.retired)
	// a mechanism running nothing of its own has nothing to retire
	(&Client{handler: plainMechanism{Handler: http.NotFoundHandler()}}).Retire(ctx)
}

// retireOrder records the order retirements happen in
type retireOrder struct {
	mu    sync.Mutex
	names []string
}

func (o *retireOrder) add(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.names = append(o.names, name)
}

func (o *retireOrder) all() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.names)
}

type orderedMechanism struct {
	plainMechanism
	order *retireOrder
}

func (m *orderedMechanism) Retire(context.Context) { m.order.add("alb") }

// orderedMember is a discovered member recording its retirement
type orderedMember struct {
	backends.Backend
	order *retireOrder
}

func (m *orderedMember) Retire(context.Context) { m.order.add(m.Name()) }

func newOrderedMember(t *testing.T, name string, order *retireOrder) *orderedMember {
	t.Helper()
	o := bo.New()
	o.Provider = providers.Prometheus
	b, err := backends.New(name, o, nil, lm.NewRouter(), nil)
	require.NoError(t, err)
	return &orderedMember{Backend: b, order: order}
}

func TestClientRetireRetiresItsMembersAfterItsMechanism(t *testing.T) {
	order := &retireOrder{}
	c := &Client{handler: &orderedMechanism{Handler: http.NotFoundHandler(), order: order}}
	c.RetireWith(nil)
	c.RetireWith(backends.Backends{"m1": newOrderedMember(t, "m1", order)})
	c.RetireWith(backends.Backends{"m2": newOrderedMember(t, "m2", order)})
	require.Empty(t, order.all(), "members handed to the ALB serve on until it retires")
	c.Retire(context.Background())
	// what the ALB's evaluator runs reads through the members, so it ends before their connections close
	got := order.all()
	require.Len(t, got, 3)
	require.Equal(t, "alb", got[0])
	require.ElementsMatch(t, []string{"m1", "m2"}, got[1:])
	// a member handed to the ALB once it has retired retires at once, and none retires twice
	c.RetireWith(backends.Backends{"m3": newOrderedMember(t, "m3", order)})
	c.Retire(context.Background())
	require.Equal(t, []string{"m3", "alb"}, order.all()[3:])
}
