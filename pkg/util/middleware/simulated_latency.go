package middleware

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const latencyHeaderName = "x-simulated-latency"

func processSimulatedLatency(w http.ResponseWriter, minMS, maxMS int) {
	if (minMS == 0 && maxMS == 0) || (minMS < 0 || maxMS < 0) {
		return
	}
	var ms int64
	if minMS >= maxMS {
		ms = int64(minMS)
	} else {
		ms = (rand.Int63() % int64(maxMS-minMS)) + int64(minMS)
	}
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	w.Header().Set(latencyHeaderName, strconv.FormatInt(ms, 10)+"ms")
}
