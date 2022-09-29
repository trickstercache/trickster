package extkube

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	v1net "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/methods"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

// ExtKube implements autodiscovery Method and holds the kubernetes interfaces needed
// to access resources.
type ExtKube struct {
	core v1core.CoreV1Interface
	net  v1net.NetworkingV1Interface
}

// kubernetes_external
func (ek *ExtKube) Name() string {
	return "kubernetes_external"
}

// Initialize ExtKube.
// We need to connect to an exterior Kubernetes cluster on this machine; the kubernetes
// API will attempt to use the local kubeconfig to do so. Failure to connect results in an error.
func (ek *ExtKube) Init() error {
	// Create a Kubernetes ClientSet from the root kubeconfig
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	// flag.Parse()
	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
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
func (ek *ExtKube) IsInitialized() bool {
	return ek.core != nil
}

// ExtKube queries MUST include a resource type and method of access.
func (ek *ExtKube) RequiredParameters() map[string][]string {
	return map[string][]string{
		"resource_type": {"service"},
		"access_by":     {"ingress"},
	}
}

// ExtKube queryies MAY include these parameters.
func (ek *ExtKube) SupportedParameters() map[string][]string {
	return map[string][]string{
		"resource_type": {"service"},
		"access_by":     {"ingress"},
		"resource_name": {"*"},
	}
}

// ExtKube queries return string values for these.
func (ek *ExtKube) SupportedResults() []string {
	return []string{
		"resource_name",
		"external_address",
	}
}

// Run a single query.
func (ek *ExtKube) Query(opts *queries.Options) ([]queries.QueryResults, error) {
	// Initialize kubernetes
	if !ek.IsInitialized() {
		ek.Init()
	}

	params := opts.Parameters
	// This doesn't map literal results, but names from method output -> query output
	// ex. external_address -> ORIGIN_URL
	resultsMap := opts.Results

	if !methods.ParametersSupported(ek, params) {
		return nil, fmt.Errorf("Query is missing required parameter")
	}

	// ===== Query Resources =====
	// This section, generally, queries a set of non-networking resources in Kubernetes and adds their
	// relevant result values to output. The next section handles networking dealies.
	output := make([]queries.QueryResults, 0)
	// QUERY RESOURCETYPE SERVICE
	if params["resource_type"] == "service" {
		if services, err := ek.core.Services("").List(context.TODO(), metav1.ListOptions{}); err != nil {
			return nil, err
		} else {
			for _, service := range services.Items {
				res := make(queries.QueryResults)
				queryMatched := true
				// Resource Name
				if resourceName, ok := params["resource_name"]; ok && resourceName != service.Name {
					queryMatched = false
				}
				if annotations, ok := params["annotations"]; ok {
					rA := service.Annotations
					// Run through annotations key:value,key:value
					alist := strings.Split(annotations, ",")
					for _, a := range alist {
						kv := strings.Split(a, ":")
						if rV, hasK := rA[kv[0]]; hasK && rV != kv[1] {
							queryMatched = false
						}
					}
				}
				// Append this result to output if the query is matched.
				if queryMatched {
					res["resource_name"] = service.Name
					output = append(output, res)
				}
			}
		}
	}
	// QUERY RESOURCETYPE POD
	if params["resource_type"] == "pod" {
		if pods, err := ek.core.Pods("").List(context.TODO(), metav1.ListOptions{}); err != nil {
			return nil, err
		} else {
			for _, pod := range pods.Items {
				res := make(queries.QueryResults)
				queryMatched := true
				// Resource Name
				if resourceName, ok := params["resource_name"]; ok && resourceName != pod.Name {
					queryMatched = false
				}
				if annotations, ok := params["annotations"]; ok {
					rA := pod.Annotations
					// Run through annotations key:value,key:value
					alist := strings.Split(annotations, ",")
					for _, a := range alist {
						kv := strings.Split(a, ":")
						if rV, hasK := rA[kv[0]]; hasK && rV != kv[1] {
							queryMatched = false
						}
					}
				}
				// Append this result to output if the query is matched.
				if queryMatched {
					res["resource_name"] = pod.Name
					output = append(output, res)
				}
			}
		}
	}

	// ===== Query Networking =====
	// We need to know how to access the resources we found meeting our query parameters.
	// Map resource names to access paths, then iterate our already-existing outputs and add the external address.
	externalPaths := make(map[string]string)
	// QUERY ACCESSBY INGRESS
	if params["access_by"] == "ingress" {
		if params["resource_type"] != "service" {
			return nil, fmt.Errorf("kubernetes_external queries with access_by:ingress must use resource_type:service")
		}
		if ingresses, err := ek.net.Ingresses("").List(context.TODO(), metav1.ListOptions{}); err != nil {
			return nil, err
		} else {
			// Iterate through each ingress retreived from the API
			for _, ingress := range ingresses.Items {
				// Iterate through ingress rules
				for _, rule := range ingress.Spec.Rules {
					// Each rule has its own host (ex. localhost, google.com)
					host := rule.Host
					// Iterate through the paths for each rule
					for _, conn := range rule.HTTP.Paths {
						path := conn.Path
						service := conn.Backend.Service.Name
						externalPaths[service] = host + path
					}
				}
			}
		}
	}
	// Iterate through exiting output, and add the external address (and any other networking values)
	for idx := range output {
		output[idx]["external_address"] = externalPaths[output[idx]["resource_name"]]
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
