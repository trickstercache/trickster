/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package kube

import (
	"errors"
	"testing"

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNew(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("expected error for nil options")
	}
	// a kubeconfig path that does not exist must error, not panic
	if _, err := New(&do.KubernetesOptions{
		Kubeconfig: "/nonexistent/kubeconfig"}); err == nil {
		t.Error("expected error for missing kubeconfig")
	}
}

func TestNewFromClientset(t *testing.T) {
	cs := fake.NewClientset()
	c := NewFromClientset(cs)
	if c.Clientset() != cs {
		t.Error("expected wrapped clientset")
	}
}

func TestDefaultNamespace(t *testing.T) {
	// outside a cluster the service account namespace file is absent
	if ns := DefaultNamespace(); ns != metav1.NamespaceDefault {
		t.Errorf("expected %q, got %q", metav1.NamespaceDefault, ns)
	}
}

func TestKlogSink(t *testing.T) {
	s := &klogSink{}
	s.Init(struct{ CallDepth int }{})
	if !s.Enabled(2) {
		t.Error("expected sink to be enabled")
	}
	// exercise the pair-building and level mapping without asserting log
	// output (the logger package owns formatting)
	s.Info(0, "informational", "key", "value", 42, "answer")
	s.Error(errors.New("boom"), "failed", "key", "value")
	s.Error(nil, "no error attached")

	named, ok := s.WithName("reflector").(*klogSink)
	if !ok || named.name != "reflector" {
		t.Fatalf("expected named sink, got %+v", named)
	}
	renamed, _ := named.WithName("watch").(*klogSink)
	if renamed.name != "reflector.watch" {
		t.Errorf("expected nested name, got %q", renamed.name)
	}
	withVals, ok := named.WithValues("a", 1).(*klogSink)
	if !ok {
		t.Fatal("expected sink from WithValues")
	}
	p := withVals.pairs([]any{"b", 2})
	for _, k := range []string{"scope", "logger", "a", "b"} {
		if _, exists := p[k]; !exists {
			t.Errorf("expected pair %q in %v", k, logging.Pairs(p))
		}
	}
}
