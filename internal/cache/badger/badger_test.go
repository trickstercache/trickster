package badger

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

func TestBadgerCache_Connect(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	// it should connect
	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBadgerCache_Store(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestBadgerCache_Retrieve(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	err = bc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := bc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func TestBadgerCache_Close(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}

	// it should close
	if err := bc.Close(); err != nil {
		t.Error(err)
	}
}
