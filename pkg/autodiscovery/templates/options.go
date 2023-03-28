package templates

type OverrideMap map[string]string

// Template options define how an autodiscovery query instantiates a new backend.
type Options struct {
	UseBackend string      `yaml:"use_backend"`
	Override   OverrideMap `yaml:"override"`
}

// New creates a new Template config object
func New() *Options {
	return &Options{
		UseBackend: "NO BACKEND",
		Override:   make(OverrideMap),
	}
}

func (opt *Options) Clone() *Options {
	overrideOut := make(map[string]string)
	for k, v := range opt.Override {
		overrideOut[k] = v
	}
	return &Options{
		UseBackend: opt.UseBackend,
		Override:   overrideOut,
	}
}
