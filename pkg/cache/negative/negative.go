// Package negative defines the Negative Cache
// which is a simple lookup map of httpStatus to TTL in milliseconds
package negative

import (
	"fmt"
	"strconv"
	"time"
)

// ConfigLookup defines a Lookup map for a collection of Named Negative Cache Configs
type ConfigLookup map[string]Config

// New returns an empty Config
func New() Config {
	return Config{}
}

// Config is a collection of response codes and their TTLs in milliseconds
type Config map[string]int

// Lookup is a collection of response codes and their TTLs as Durations
type Lookup map[int]time.Duration

// Lookups is a collection of Lookup maps
type Lookups map[string]Lookup

// Clone returns an exact copy of a Config
func (nc Config) Clone() Config {
	nc2 := make(Config)
	for k, v := range nc {
		nc2[k] = v
	}
	return nc2
}

// Get returns the named Lookup from the Lookups collection if it exists
func (l Lookups) Get(name string) Lookup {
	if v, ok := l[name]; ok {
		return v
	}
	return nil
}

// Validate verifies the Negative Cache Config
func (l ConfigLookup) Validate() (Lookups, error) {
	ml := make(Lookups)
	if len(l) == 0 {
		return ml, nil
	}
	for k, n := range l {
		lk := make(Lookup)
		for c, t := range n {
			ci, err := strconv.Atoi(c)
			if err != nil {
				return nil, fmt.Errorf(`invalid negative cache config in %s: %s is not a valid status code`, k, c)
			}
			if ci < 400 || ci >= 600 {
				return nil, fmt.Errorf(`invalid negative cache config in %s: %s is not >= 400 and < 600`, k, c)
			}
			lk[ci] = time.Duration(t) * time.Millisecond
		}
		ml[k] = lk
	}
	return ml, nil
}
