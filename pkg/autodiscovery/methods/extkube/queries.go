package extkube

/*
// Query services with a set of query parameters
// Returns a []query.QueryResults with resource_name filled for each service
func (ek *ExtKube) queryServices(params queries.QueryParameters) ([]queries.QueryResults, error) {
	output := make([]queries.QueryResults, 0)
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
	return output, nil
}

// Queries all ingresses in the cluster for paths by the service name that they point to.
func (ek *ExtKube) queryIngressPathsByService(_ queries.QueryParameters) (map[string]string, error) {
	externalPaths := make(map[string]string)
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
	return externalPaths, nil
}
*/
