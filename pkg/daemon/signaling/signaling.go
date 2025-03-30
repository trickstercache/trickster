package signaling

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/trickstercache/trickster/v2/pkg/config/reload"
)

func Wait(reloader reload.ReloadFunc) {
	// Serve with Config
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-sigs
		if sig == syscall.SIGHUP {
			reloader()
		} else if sig == syscall.SIGINT || sig == syscall.SIGTERM {
			break
		}
	}
}
