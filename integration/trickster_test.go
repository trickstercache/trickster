package integration

import (
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Test Trickster capabilities common to all backends / caches / configurations.
func TestTrickster(t *testing.T) {
	t.Run("config not found", func(t *testing.T) {
		// Simple test to ensure trickster returns an error if its config is not found.
		ctx := context.Background()
		expected := expectedStartError{
			ErrorContains: pointers.New("open testdata/cfg-notfound.yaml: no such file or directory"),
		}
		startTrickster(t, ctx, expected, "-config", "testdata/cfg-notfound.yaml")
	})
	t.Run("start and stop", func(t *testing.T) {
		// Simple test to ensure that Trickster can start and be stopped within a test.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		started := make(chan struct{})
		go func() { // wait for trickster to start
			time.Sleep(5 * time.Second) // TODO: remove sleep & return explicit start signal
			checkTricksterMetrics(t, "localhost:8480")
			started <- struct{}{}
		}()
		go startTrickster(t, ctx, expectedStartError{}, "-config", "../docs/developer/environment/trickster-config/trickster.yaml")
		<-started
		t.Log("started...")
		metrics := checkTricksterMetrics(t, "localhost:8480")
		t.Log("Trickster metrics:", metrics)
	})
}
