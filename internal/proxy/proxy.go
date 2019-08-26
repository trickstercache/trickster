package proxy

import (
	"fmt"
	"net"

	"golang.org/x/net/netutil"

	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

// NewListener create a new network listener which obeys to the configuration max
// connection limit, and also monitors connections with prometheus metrics.
//
// The way this works is by creating a listener and wrapping it with a
// netutil.LimitListener to set a limit.
//
// This limiter will simply block waiting for resources to become available
// whenever clients go above the limit.
//
// To simplify settings limits the listener is wrapped with yet another object
// which observes the connections to set a gauge with the current number of
// connections (with operates with sampling through scrapes), and a set of
// counter metrics for connections accepted, rejected and closed.
func NewListener(listenAddress string, listenPort, connectionsLimit int) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", listenAddress, listenPort))
	if err != nil {
		// so we can exit one level above, this usually means that the port is in use
		return nil, err
	}

	log.Debug("starting http proxy listener", log.Pairs{
		"connectionsLimit": connectionsLimit,
		"address":          listenAddress,
		"port":             listenPort,
	})

	if connectionsLimit > 0 {
		listener = netutil.LimitListener(listener, connectionsLimit)
		metrics.ProxyMaxConnections.Set(float64(connectionsLimit))
	}

	return &connectionsLimitObProxy{
		listener,
	}, nil
}

type connectionsLimitObProxy struct {
	net.Listener
}

// Accept implements Listener.Accept
func (l *connectionsLimitObProxy) Accept() (net.Conn, error) {

	metrics.ProxyConnectionRequested.Inc()

	c, err := l.Listener.Accept()
	if err != nil {
		// This generally happens when a connection gives up waiting for resources and
		// just goes away on timeout, thus it's more of a client side error, which
		// gets reflected on the server.
		log.Debug("failed to accept client connection", log.Pairs{"reason": err})
		metrics.ProxyConnectionFailed.Inc()
		return c, err
	}

	metrics.ProxyActiveConnections.Inc()
	metrics.ProxyConnectionAccepted.Inc()

	return observedConnection{c}, nil
}

type observedConnection struct {
	net.Conn
}

func (o observedConnection) Close() error {
	err := o.Conn.Close()

	metrics.ProxyActiveConnections.Dec()
	metrics.ProxyConnectionClosed.Inc()

	return err
}
