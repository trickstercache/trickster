package intkube

/*
import (
	"context"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Query services with a set of query parameters
// Returns a []query.QueryResults with resource_name filled for each service
func (ik *IntKube) queryServices(params queries.QueryParameters) ([]queries.QueryResults, error) {
	output := make([]queries.QueryResults, 0)
	if services, err := ik.core.Services("").List(context.TODO(), metav1.ListOptions{}); err != nil {
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
				res["cluster_ip"] = service.Spec.ClusterIP
				output = append(output, res)
			}
		}
	}
	return output, nil
}

func (ik *IntKube) queryPods(params queries.QueryParameters) ([]queries.QueryResults, error) {
	output := make([]queries.QueryResults, 0)
	if pods, err := ik.core.Pods("").List(context.TODO(), metav1.ListOptions{}); err != nil {
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
				res["cluster_ip"] = pod.Status.PodIP
				output = append(output, res)
			}
		}
	}
	return output, nil
}
*/
