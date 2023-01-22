package intkube

/*

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	v1net "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

// IntKube implements autodiscovery Method and holds the kubernetes interfaces needed
// to access resources.
type IntKube struct {
	core v1core.CoreV1Interface
	net  v1net.NetworkingV1Interface
}

// kubernetes_external
func (ek *IntKube) Name() string {
	return "kubernetes_internal"
}

// Initialize IntKube.
// We need to connect to an exterior Kubernetes cluster on this machine; the kubernetes
// API will attempt to use the local kubeconfig to do so. Failure to connect results in an error.
func (ek *IntKube) Init() error {
	// flag.Parse()
	// Get the config for a pod in the cluster
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	ek.core = clientset.CoreV1()
	ek.net = clientset.NetworkingV1()
	return nil
}

// Interfaces must be non-nil.
func (ek *IntKube) IsInitialized() bool {
	return ek.core != nil
}

// IntKube queries MUST include a resource type and method of access.
func (ek *IntKube) RequiredParameters() map[string][]string {
	return map[string][]string{
		"resource_type": {"service"},
		"access_by":     {"ingress"},
	}
}

// IntKube queries MAY include these parameters.
func (ek *IntKube) SupportedParameters() map[string][]string {
	return map[string][]string{
		"resource_type": {"service"},
		"access_by":     {"ingress"},
		"resource_name": {"*"},
	}
}

// IntKube queries return string values for these.
func (ek *IntKube) SupportedResults() []string {
	return []string{
		"resource_name",
		"external_address",
	}
}

// Run a single query.
func (ik *IntKube) Query(opts *queries.Options) ([]queries.QueryResults, error) {
	// Initialize kubernetes
	if !ik.IsInitialized() {
		ik.Init()
	}

	params := opts.Parameters
	// This doesn't map literal results, but names from method output -> query output
	// ex. external_address -> ORIGIN_URL
	resultsMap := opts.Results

	if !methods.ParametersSupported(ik, params) {
		return nil, fmt.Errorf("Query is missing required parameter")
	}

	// ===== Query Resources =====
	// This section, generally, queries a set of non-networking resources in Kubernetes and adds their
	// relevant result values to output. The next section handles networking dealies.
	output := make([]queries.QueryResults, 0)
	var err error
	// QUERY RESOURCETYPE SERVICE
	if params["resource_type"] == "service" {
		output, err = ik.queryServices(params)
		if err != nil {
			return nil, err
		}
	}
	// QUERY RESOURCETYPE POD
	if params["resource_type"] == "pod" {
		output, err = ik.queryPods(params)
		if err != nil {
			return nil, err
		}
	}

	// Output now contains the resources from kubernetes_external query; need to use resultMap to
	// get our template values
	templateValues := make([]queries.QueryResults, 0)
	for idx := range output {
		templateValues = append(templateValues, make(queries.QueryResults))
		for queryKey, queryValue := range output[idx] {
			if templateKey, ok := resultsMap[queryKey]; ok {
				templateValues[idx][templateKey] = queryValue
			}
		}
	}

	return templateValues, nil
}

*/
