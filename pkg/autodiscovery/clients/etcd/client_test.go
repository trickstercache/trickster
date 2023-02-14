package etcd

import (
	"context"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/etcd"
)

// etcd testing requires a local etcd cluster.

var testQuery *etcd.Query = &etcd.Query{
	UseTemplate: "",
	Keys:        []string{"trickster-validate-connection"},
}

func TestEtcdClient(t *testing.T) {
	client := New()
	client.Endpoints = []string{"http://localhost:2379", "http://invalid.com"}
	err := client.Connect()
	if err != nil {
		t.Fatal(err)
	}
	// Directly set a test value for testQuery
	client.client.Put(context.Background(), "trickster-validate-connection", "OK")
	defer client.client.Delete(context.Background(), "trickster-validate-connection")
	qres, err := client.Execute(testQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(qres) != 1 {
		t.Fatalf("query should have returned exactly one result")
	}
	if ttk, ok := qres[0]["trickster-validate-connection"]; !ok {
		t.Fatal("query should have a value for trickster-validate-connection")
	} else if ttk != "OK" {
		t.Fatalf("query should have OK for trickster-validate-connection")
	}
}
