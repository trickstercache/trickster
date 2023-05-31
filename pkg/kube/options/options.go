package options

type Options struct {
	// InCluster should be true if Trickster is running inside the target cluster.
	// Otherwise, set to false and use ConfigPath to point to a .kubeconfig.
	InCluster bool `yaml:"in_cluster"`
	// ConfigPath should be an absolute path to a kubeconfig file.
	// Ignored if InCluster is true.
	ConfigPath string `yaml:"config_path"`
}
