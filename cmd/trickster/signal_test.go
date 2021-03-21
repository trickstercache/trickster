package main

import (
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/cmd/trickster/config"
	"github.com/tricksterproxy/trickster/pkg/observability/logging"
)

func TestStartHupMonitor(t *testing.T) {

	w := httptest.NewRecorder()
	logger := logging.StreamLogger(w, "WARN")

	// passing case is the call returns immediately without starting the goroutine
	startHupMonitor(nil, nil, nil, nil, nil)

	qch := make(chan bool)
	conf := config.NewConfig()
	conf.Resources = &config.Resources{QuitChan: qch}
	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	qch <- true

	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)

	logger.Close()

	w = httptest.NewRecorder()
	logger = logging.StreamLogger(w, "WARN")

	now := time.Unix(1577836800, 0)
	nowMinus1m := time.Now().Add(-1 * time.Minute)
	conf.Main.SetStalenessInfo("../../testdata/test.empty.conf", now, nowMinus1m)
	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)
	t.Error()

}
