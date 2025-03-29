package signaling

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/trickstercache/trickster/v2/pkg/server/instance"
	"github.com/trickstercache/trickster/v2/pkg/server/reload"
)

func Wait(si *instance.ServerInstance) {
	// Serve with Config
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-sigs
		switch sig {
		case syscall.SIGHUP:
			reload.RequestReload(si)
		case syscall.SIGINT, syscall.SIGTERM:
			break
		}
	}
}
