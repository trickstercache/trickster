package main

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/bbolt"
)

func main() {

	const secs time.Duration = 3
	const ttl = time.Second * secs

	const myKey, myValue = "myKeyName", "myValue"

	// create a new memory cache
	c, err := bbolt.New("/Users/jranson/test.bbolt", "trickster")
	// this starts some background goroutines for object lifecycle management, so
	// be sure to Close() once ready for the cache to be garbage collected
	if err != nil {
		panic(err)
	}

	// Optional - Set the max cache size (default is 512MB):
	cfg := c.Configuration()
	cfg.Index.MaxSizeBytes = 4 * 1024 * 1024 * 1024 // 4GB
	//

	// store an object in the cache with 3s TTL
	fmt.Printf("Storing w/ TTL: key=[%s] value=[%s] ttl=[%s]\n", myKey, myValue, ttl.String())
	err = c.Store(myKey, []byte(myValue), ttl)
	if err != nil {
		panic(err)
	}

	// store an object in the cache with no TTL
	key2 := myKey + "2"
	val2 := myValue + "2"
	fmt.Printf("Storing no TTL: key=[%s] value=[%s] ttl=[0]\n", key2, val2)
	err = c.Store(key2, []byte(val2), 0)
	if err != nil {
		panic(err)
	}

	// retrieve the object from cache
	value, status, err := c.Retrieve(myKey, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Retrieve: key=[%s] status=[%s] value=[%s]\n",
		myKey, status.String(), string(value))

	// retrieve the second object from cache
	value, status, err = c.Retrieve(key2, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Retrieve: key=[%s] status=[%s] value=[%s]\n",
		myKey, status.String(), string(value))

	// sleep slightly longer than the TTL so the object has been reaped
	fmt.Printf("sleeping %d.25 seconds\n", secs)
	time.Sleep(ttl + (time.Millisecond * 250))

	// retrieve the ttl'ed object from cache again, should be a cache miss
	value, status, err = c.Retrieve(myKey, false)
	if err != nil && err != cache.ErrKNF {
		panic(err)
	}
	fmt.Printf("2nd Retrieve: key=[%s] status=[%s] value=[%s]\n",
		myKey, status.String(), string(value))

	// retrieve the non-ttl'ed object from cache again, should still cache hit
	value, status, err = c.Retrieve(key2, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("2nd Retrieve: key=[%s] status=[%s] value=[%s]\n",
		key2, status.String(), string(value))

	// close the cache when you're ready for it to be garbage collected,
	// or the background goroutines will continue to run
	c.Close()

}
