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

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

func TestResolveSlicePort(t *testing.T) {
	ports := []discoveryv1.EndpointPort{
		{Name: new("web"), Port: new(int32(8080)), AppProtocol: new("https")},
		{Name: new("metrics"), Port: new(int32(9090))},
	}
	// named
	p, app, ok := resolveSlicePort(ports, "web")
	require.True(t, ok)
	require.Equal(t, int32(8080), p)
	require.Equal(t, "https", app)
	// numeric, matching a declared port adopts its appProtocol
	p, app, ok = resolveSlicePort(ports, "8080")
	require.True(t, ok)
	require.Equal(t, int32(8080), p)
	require.Equal(t, "https", app)
	// numeric, undeclared: used as-is
	p, app, ok = resolveSlicePort(ports, "9999")
	require.True(t, ok)
	require.Equal(t, int32(9999), p)
	require.Empty(t, app)
	// empty with multiple declared ports: ambiguous
	_, _, ok = resolveSlicePort(ports, "")
	require.False(t, ok)
	// empty with one declared port
	p, _, ok = resolveSlicePort(ports[:1], "")
	require.True(t, ok)
	require.Equal(t, int32(8080), p)
	// unknown name
	_, _, ok = resolveSlicePort(ports, "nope")
	require.False(t, ok)
	// no ports at all
	_, _, ok = resolveSlicePort(nil, "")
	require.False(t, ok)
}

func TestResolveServicePort(t *testing.T) {
	ports := []corev1.ServicePort{
		{Name: "web", Port: 80, AppProtocol: new("https")},
		{Name: "metrics", Port: 9090},
	}
	p, app, ok := resolveServicePort(ports, "web")
	require.True(t, ok)
	require.Equal(t, int32(80), p)
	require.Equal(t, "https", app)
	p, _, ok = resolveServicePort(ports, "9090")
	require.True(t, ok)
	require.Equal(t, int32(9090), p)
	_, _, ok = resolveServicePort(ports, "")
	require.False(t, ok, "two ports with no query port is ambiguous")
	p, _, ok = resolveServicePort(ports[1:], "")
	require.True(t, ok)
	require.Equal(t, int32(9090), p)
	_, _, ok = resolveServicePort(ports, "nope")
	require.False(t, ok)
}

func TestResolvePodPort(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080}},
		}}},
	}
	p, ok := resolvePodPort(pod, "web")
	require.True(t, ok)
	require.Equal(t, int32(8080), p)
	p, ok = resolvePodPort(pod, "9999")
	require.True(t, ok)
	require.Equal(t, int32(9999), p)
	p, ok = resolvePodPort(pod, "")
	require.True(t, ok, "sole declared container port is unambiguous")
	require.Equal(t, int32(8080), p)
	_, ok = resolvePodPort(pod, "nope")
	require.False(t, ok)
	pod.Spec.Containers[0].Ports = append(pod.Spec.Containers[0].Ports,
		corev1.ContainerPort{Name: "metrics", ContainerPort: 9090})
	_, ok = resolvePodPort(pod, "")
	require.False(t, ok, "multiple declared ports with no query port is ambiguous")
}

func TestResolveScheme(t *testing.T) {
	require.Equal(t, "https", resolveScheme("https", "", nil),
		"query scheme wins")
	require.Equal(t, "http", resolveScheme("http", "https", nil),
		"query scheme wins over appProtocol")
	require.Equal(t, "https", resolveScheme("", "https", nil))
	require.Equal(t, "https", resolveScheme("", "",
		map[string]string{AnnotationScheme: "https"}))
	require.Equal(t, "http", resolveScheme("", "",
		map[string]string{AnnotationScheme: "gopher"}),
		"unknown annotation values are ignored")
	require.Equal(t, "http", resolveScheme("", "", nil))
}
