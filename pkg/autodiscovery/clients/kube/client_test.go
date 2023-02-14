package kube

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/queries/kube"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testQueryPod *kube.Query = &kube.Query{
	Namespace:     "trickster-validate",
	ResourceKinds: []string{"Pod"},
	ResourceName:  "validation-pod",
	AccessBy:      "resource_ip",
}

func TestKubeExternalClient_Pod(t *testing.T) {
	client := New()
	client.InCluster = false
	client.ConfigPath = "../../../../testdata/test.kubeconfig_cicd.conf"
	err := client.Connect()
	if err != nil {
		t.Fatal(err)
	}
	// Get trickster-validate namespace
	ctx := context.Background()
	ctxto, cancel := context.WithTimeout(ctx, time.Second)
	n, err := client.core.Namespaces().Get(ctxto, "trickster-validate", metav1.GetOptions{})
	cancel()
	if err != nil || n == nil {
		ctxto, cancel = context.WithTimeout(ctx, time.Second*5)
		_, err := client.core.Namespaces().Create(
			ctxto, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "trickster-validate"}}, metav1.CreateOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	ctxto, cancel = context.WithTimeout(ctx, time.Second*5)
	_, err = client.core.Pods("trickster-validate").Create(
		ctxto,
		&v1.Pod{
			Spec: v1.PodSpec{
				Containers: []v1.Container{{Name: "sleep", Image: "alpine"}},
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "validation-pod",
			},
		},
		metav1.CreateOptions{},
	)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	// Try to access validation-pod
	res, err := client.Execute(testQueryPod)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(res)
}
