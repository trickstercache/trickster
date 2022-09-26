package templates

type OverrideMap map[string]string

// Template options define how an autodiscovery query instantiates a new backend.
type Options struct {
	UseBackend string      `yaml:"use_backend"`
	Override   OverrideMap `yaml:"override"`
}

// Create a new Template config object
func New() *Options {
	return &Options{
		UseBackend: "NO BACKEND",
		Override:   make(OverrideMap),
	}
}
