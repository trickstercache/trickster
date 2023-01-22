package methods

/*
import (
	"fmt"
)

// Const type for AutodiscoveryMethods
type AutodiscoveryMethod int

const (
	// Discover by kubernetes annotation
	MOCK = AutodiscoveryMethod(iota)
	EXTKUBE
	INTKUBE
)

// Names maps string config names to constant AutodiscoveryMethod values.
var Names = map[string]AutodiscoveryMethod{
	"mock":                MOCK,
	"kubernetes_external": EXTKUBE,
	"kubernetes_internal": INTKUBE,
}

// Values maps constant AutodiscoveryMethod values to string config names.
var Values = make(map[AutodiscoveryMethod]string)

// Methods maps AutodiscoveryMethod consts to their Method.
var Methods = make(map[AutodiscoveryMethod]Method)

// Run on initialization; reverse map Names to Values and create empty Methods
func init() {
	for name, method := range Names {
		Values[method] = name
	}
}

// Query if a string name represents a supported autodiscovery method
func IsSupportedADMethod(name string) bool {
	_, ok := Names[name]
	return ok
}

// Get an autodiscovery method by name.
func GetMethod(name string) (Method, error) {
	if mid, ok := Names[name]; !ok {
		return nil, fmt.Errorf("%s is not a valid method name", name)
	} else {
		if m, ok := Methods[mid]; !ok {
			return nil, fmt.Errorf("%s is a valid method name, but has no method attached to it.", name)
		} else {
			return m, nil
		}
	}
}
*/
