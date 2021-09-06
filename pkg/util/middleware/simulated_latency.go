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
	if minMS >= maxMS {
		w.Header().Set(latencyHeaderName, strconv.Itoa(minMS)+"ms")
		time.Sleep(time.Duration(minMS) * time.Millisecond)
		return
	}
	diff := int64(maxMS - minMS)
	v := (rand.Int63() % diff) + int64(minMS)
	time.Sleep(time.Duration(v) * time.Millisecond)
	w.Header().Set(latencyHeaderName, strconv.FormatInt(v, 10)+"ms")
}
