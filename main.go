package main

// Trickster - Reverse Proxy Cache for Prometheus HTTP API

import (
	"fmt"
	"net/http"
	"os"

	"github.com/go-kit/kit/log/level"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

const progversion = "0.0.11"

// main function to boot up everything
func main() {

	t := &TricksterHandler{}
	t.ResponseChannels = make(map[string]chan *ClientRequestContext)

	t.Config = NewConfig()
	if err := loadConfiguration(t.Config, os.Args[1:]); err != nil {
		// using fmt.Println because logger can't be instantiated without the config loaded
		// to know the log path, and the config load just failed, so we just abort.
		fmt.Println("Could not load trickster configuration: ", err.Error())
		os.Exit(1)
	}

	if t.Config.Main.InstanceID > 0 {
		t.Logger = newLogger(t.Config.Logging, fmt.Sprint(t.Config.Main.InstanceID))
	} else {
		t.Logger = newLogger(t.Config.Logging, "")
	}

	level.Info(t.Logger).Log("event", "application startup", "version", progversion)

	t.Metrics = NewApplicationMetrics(t.Config, t.Logger)

	t.Cacher = getCache(t)
	if err := t.Cacher.Connect(); err != nil {
		level.Error(t.Logger).Log("event", "Unable to connect to Cache", "detail", err.Error())
		os.Exit(1)
	}
	defer t.Cacher.Close()

	router := mux.NewRouter()

	// API Version 1 Support
	const apiV1Path = "/api/v1/"

	// Health Check Paths
	router.HandleFunc("/{originMoniker}/"+mnHealth, t.promHealthCheckHandler).Methods("GET")
	router.HandleFunc("/"+mnHealth, t.promHealthCheckHandler).Methods("GET")

	// Path-based  multi-origin support - no support for full proxy of the prometheus UI, only querying
	router.HandleFunc("/{originMoniker}"+apiV1Path+mnQueryRange, t.promQueryRangeHandler).Methods("GET")
	router.HandleFunc("/{originMoniker}"+apiV1Path+mnQuery, t.promQueryHandler).Methods("GET")
	router.PathPrefix("/{originMoniker}" + apiV1Path).HandlerFunc(t.promAPIProxyHandler).Methods("GET")

	router.HandleFunc(apiV1Path+mnQueryRange, t.promQueryRangeHandler).Methods("GET")
	router.HandleFunc(apiV1Path+mnQuery, t.promQueryHandler).Methods("GET")
	router.PathPrefix(apiV1Path).HandlerFunc(t.promAPIProxyHandler).Methods("GET")

	// Catch All for Single-Origin proxy
	router.PathPrefix("/").HandlerFunc(t.promFullProxyHandler).Methods("GET")

	level.Info(t.Logger).Log("event", "proxy http endpoint starting", "port", t.Config.ProxyServer.ListenPort)

	// Start the Server
	err := http.ListenAndServe(fmt.Sprintf(":%d", t.Config.ProxyServer.ListenPort), handlers.CompressHandler(router))
	level.Error(t.Logger).Log("event", "exiting", "err", err)
}
