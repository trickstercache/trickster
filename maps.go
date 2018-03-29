package main

var supportedParameters = map[string]bool{
	// common
	upQuery:      true,
	upOriginFqdn: true,
	upOriginPort: true,

	// query_range
	upStart: true,
	upEnd:   true,
	upStep:  true,
}

var timeMultipliers = map[string]int64{
	"s": 1,
	"m": 60,
	"h": 60 * 60,
	"d": 60 * 60 * 24,
	"w": 60 * 60 * 24 * 7,
}
