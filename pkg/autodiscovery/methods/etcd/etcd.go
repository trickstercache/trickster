package etcd

/*
import (
	"context"
	"errors"

	"github.com/coreos/etcd/clientv3"
	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries"
)

type etcd struct {
	client *clientv3.Client
}

// Name gives the config name of the autodiscovery method.
func (method *etcd) Name() string

// Init should initialize a struct, starting from empty, ex.
//
//	method = &KubeExt2{}.Init()
//
// Methods should not do anything before Init() is called.
func (method *etcd) Init() error {
	cli, err := clientv3.New(clientv3.Config{})
	if err != nil {
		return err
	}
	method.client = cli
	return nil
}

// IsInitialized should return if the method has had Init() called yet.
func (method *etcd) IsInitialized() bool {
	return method.client != nil
}

// RequiredParameters maps each required query parameter to its supported string values.
// For a query to be valid, every required parameter must be included with a supported value.
// Required parameters cannot have wildcard values.
func (method *etcd) RequiredParameters() map[string][]string {
	return map[string][]string{
		"endpoint": {"*"},
		"key":      {"*"},
	}
}

// SupportedParameters maps each supported query parameter for the method to a slice
// of supported string values. For a query to be valid, every parameter and its value must
// be supported.
func (method *etcd) SupportedParameters() map[string][]string {
	return map[string][]string{
		"endpoint": {"*"},
		"key":      {"*"},
	}
}

// SupportedResults lists the supported result keys for the method.
// Results must have a value for every supported result key.
func (method *etcd) SupportedResults() []string {
	return []string{
		"value",
	}
}

func (method *etcd) Query(opts *queries.Options) ([]queries.QueryResults, error) {
	if !method.IsInitialized() {
		method.Init()
	}
	params := opts.Parameters
	resultsMap := opts.Results

	// This is much simpler than Kubernetes:
	// 1. Connect to endpoint
	// 2. Query for key
	// 3. Return value as resultsMap["value"]
	method.client.SetEndpoints(params["endpoint"])
	res, err := method.client.KV.Get(context.TODO(), params["key"])
	if err != nil {
		return nil, err
	}
	if len(res.Kvs) == 0 {
		return nil, errors.New("etcd query returned 0 kvs")
	}

	out := make([]queries.QueryResults, 0)
	outKey, ok := resultsMap["value"]
	if !ok {
		outKey = "value"
	}
	for idx := range res.Kvs {
		out = append(out, make(queries.QueryResults))
		out[idx][outKey] = string(res.Kvs[idx].Value)
	}
	return out, nil
}
*/
