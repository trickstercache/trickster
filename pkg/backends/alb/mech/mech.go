package mech

import "strconv"

// Mechanism defines the load balancing mechanism identifier type
type Mechanism byte

const (
	// RoundRobin defines the Basic Round Robin load balancing mechanism
	RoundRobin Mechanism = iota
	// FirstResponse defines the First Response load balancing mechanism
	FirstResponse
	// FirstGoodResponse defines the First Good Response load balancing mechanism
	FirstGoodResponse
	// NewestLastModified defines the Newest Last-Modified load balancing mechanism
	NewestLastModified
	// TimeSeriesMerge defines the Time Series Merge load balancing mechanism
	TimeSeriesMerge
	// UserRouter defines the User Router load balancing mechanism
	UserRouter

	RR  = "rr"
	FR  = "fr"
	FGR = "fgr"
	NLM = "nlm"
	TSM = "tsm"
	UR  = "ur"
)

// Lookup provides for looking up Mechanisms by name
var Lookup = map[string]Mechanism{
	RR:  RoundRobin,
	FR:  FirstResponse,
	FGR: FirstGoodResponse,
	NLM: NewestLastModified,
	TSM: TimeSeriesMerge,
	UR:  UserRouter,
}

// ValuesLookup provides for looking up Mechanism by names
var ValuesLookup = vals()

// GetMechanismByName returns the Mechanism value and True if the mechanism name is known
func GetMechanismByName(name string) (Mechanism, bool) {
	m, ok := Lookup[name]
	return m, ok
}

func vals() map[Mechanism]string {
	out := make(map[Mechanism]string, len(Lookup))
	for k, v := range Lookup {
		out[v] = k
	}
	return out
}

func (m Mechanism) String() string {
	if v, ok := ValuesLookup[m]; ok {
		return v
	}
	return strconv.Itoa(int(m))
}
