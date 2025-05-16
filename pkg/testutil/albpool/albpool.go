package albpool

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func New(m mech.Mechanism, healthyFloor int, hs []http.Handler) (pool.Pool,
	[]*pool.Target, []*healthcheck.Status) {
	var targets []*pool.Target
	var statuses []*healthcheck.Status
	if len(hs) > 0 {
		targets = make([]*pool.Target, 0, len(hs))
		statuses = make([]*healthcheck.Status, 0, len(hs))
		for _, h := range hs {
			hst := &healthcheck.Status{}
			statuses = append(statuses, hst)
			targets = append(targets, pool.NewTarget(h, hst))
		}
	}
	pool := pool.New(m, targets, healthyFloor)
	return pool, targets, statuses
}
