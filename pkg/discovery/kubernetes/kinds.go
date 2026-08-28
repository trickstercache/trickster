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
	"net"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
)

// AnnotationScheme is the well-known annotation consulted on watched
// Services and Pods to select the member scheme when the query does not set
// one (e.g. trickster.io/scheme: https)
const AnnotationScheme = "trickster.io/scheme"

// buildEndpointSlices maps the ready endpoint addresses of the query's
// Service onto members. Terminating endpoints are omitted entirely so
// members leave the pool (and drain) before kubelet kills their pod; the
// ready condition maps to the member ReadyState (nil is interpreted as
// ready, per the EndpointSlice API convention).
func (s *subscription) buildEndpointSlices(lister discoverylisters.EndpointSliceNamespaceLister) discovery.Snapshot {
	slices, err := lister.List(labels.Everything())
	if err != nil {
		s.warnBuild("error listing endpointslices", err)
		return nil
	}
	out := make(discovery.Snapshot, 0, len(slices)*2)
	for _, slice := range slices {
		if slice == nil {
			continue
		}
		port, appProto, ok := resolveSlicePort(slice.Ports, s.q.Port)
		if !ok {
			s.warnPort("endpointslice", slice.Name, len(slice.Ports))
			continue
		}
		scheme := resolveScheme(s.q.Scheme, appProto, nil)
		for i := range slice.Endpoints {
			ep := &slice.Endpoints[i]
			if len(ep.Addresses) == 0 {
				continue
			}
			if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
				continue
			}
			state := discovery.NotReady
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				state = discovery.Ready
			}
			name := ep.Addresses[0]
			var group string
			if ep.TargetRef != nil && ep.TargetRef.Name != "" {
				name = ep.TargetRef.Name
				group = s.podReplicaGroup(ep.TargetRef.Name)
			}
			out = append(out, discovery.Member{
				Name:         name,
				Scheme:       scheme,
				Address:      joinHostPort(ep.Addresses[0], port),
				Ready:        state,
				ReplicaGroup: group,
				Labels: map[string]string{
					"namespace": slice.Namespace,
					"service":   s.q.Service,
				},
			})
		}
	}
	s.clearPortWarn()
	return out
}

// buildServices maps each matching Service's ClusterIP onto one member.
// Headless and unallocated services are skipped. Services convey no
// readiness, so members report ReadyUnknown.
func (s *subscription) buildServices(lister corelisters.ServiceNamespaceLister) discovery.Snapshot {
	svcs, err := lister.List(labels.Everything())
	if err != nil {
		s.warnBuild("error listing services", err)
		return nil
	}
	out := make(discovery.Snapshot, 0, len(svcs))
	for _, svc := range svcs {
		if svc == nil || svc.Spec.ClusterIP == "" ||
			svc.Spec.ClusterIP == corev1.ClusterIPNone {
			continue
		}
		port, appProto, ok := resolveServicePort(svc.Spec.Ports, s.q.Port)
		if !ok {
			s.warnPort("service", svc.Name, len(svc.Spec.Ports))
			continue
		}
		out = append(out, discovery.Member{
			Name:         svc.Name,
			Scheme:       resolveScheme(s.q.Scheme, appProto, svc.Annotations),
			Address:      joinHostPort(svc.Spec.ClusterIP, port),
			Ready:        discovery.ReadyUnknown,
			ReplicaGroup: s.labelReplicaGroup(svc.Labels),
			Labels:       map[string]string{"namespace": svc.Namespace},
		})
	}
	s.clearPortWarn()
	return out
}

// buildPods maps the pod IPs of Pods matching the label selector onto
// members. Pods that are not Running, have no IP, or are terminating
// (deletion timestamp set) are omitted; readiness comes from the PodReady
// condition.
func (s *subscription) buildPods(lister corelisters.PodNamespaceLister) discovery.Snapshot {
	pods, err := lister.List(labels.Everything())
	if err != nil {
		s.warnBuild("error listing pods", err)
		return nil
	}
	out := make(discovery.Snapshot, 0, len(pods))
	for _, pod := range pods {
		if pod == nil || pod.Status.PodIP == "" ||
			pod.Status.Phase != corev1.PodRunning ||
			pod.DeletionTimestamp != nil {
			continue
		}
		port, ok := resolvePodPort(pod, s.q.Port)
		if !ok {
			s.warnPort("pod", pod.Name, 0)
			continue
		}
		state := discovery.NotReady
		for i := range pod.Status.Conditions {
			c := &pod.Status.Conditions[i]
			if c.Type == corev1.PodReady {
				if c.Status == corev1.ConditionTrue {
					state = discovery.Ready
				}
				break
			}
		}
		out = append(out, discovery.Member{
			Name:         pod.Name,
			Scheme:       resolveScheme(s.q.Scheme, "", pod.Annotations),
			Address:      joinHostPort(pod.Status.PodIP, port),
			Ready:        state,
			ReplicaGroup: s.labelReplicaGroup(pod.Labels),
			Labels:       map[string]string{"namespace": pod.Namespace},
		})
	}
	s.clearPortWarn()
	return out
}

// labelReplicaGroup reads the member's TSM replica group from the watched
// object's labels when the query configures replica_group_label; an absent
// label yields no group (template semantics apply)
func (s *subscription) labelReplicaGroup(objLabels map[string]string) string {
	if s.q.ReplicaGroupLabel == "" {
		return ""
	}
	return objLabels[s.q.ReplicaGroupLabel]
}

// podReplicaGroup resolves an endpoint's replica group from its target
// pod's labels via the joined pod informer
func (s *subscription) podReplicaGroup(podName string) string {
	if s.q.ReplicaGroupLabel == "" || s.podLister == nil {
		return ""
	}
	pod, err := s.podLister.Get(podName)
	if err != nil || pod == nil {
		// pod not (yet) in the cache: the member joins without a group and
		// regroups on the pod informer's add event
		return ""
	}
	return pod.Labels[s.q.ReplicaGroupLabel]
}

// resolveSlicePort selects the member port from an EndpointSlice's declared
// ports: a numeric query port is used as-is (adopting a matching declared
// port's appProtocol), a named query port must match a declared port name,
// and an empty query port requires exactly one declared port
func resolveSlicePort(ports []discoveryv1.EndpointPort, want string) (int32, string, bool) {
	if n, ok := portNumber(want); ok {
		for i := range ports {
			if ports[i].Port != nil && *ports[i].Port == n {
				return n, strValue(ports[i].AppProtocol), true
			}
		}
		return n, "", true
	}
	if want != "" {
		for i := range ports {
			if ports[i].Name != nil && *ports[i].Name == want && ports[i].Port != nil {
				return *ports[i].Port, strValue(ports[i].AppProtocol), true
			}
		}
		return 0, "", false
	}
	var found *discoveryv1.EndpointPort
	for i := range ports {
		if ports[i].Port == nil {
			continue
		}
		if found != nil {
			return 0, "", false // ambiguous: multiple ports, none selected
		}
		found = &ports[i]
	}
	if found == nil {
		return 0, "", false
	}
	return *found.Port, strValue(found.AppProtocol), true
}

// resolveServicePort selects the member port from a Service's ports using
// the same rules as resolveSlicePort
func resolveServicePort(ports []corev1.ServicePort, want string) (int32, string, bool) {
	if n, ok := portNumber(want); ok {
		for i := range ports {
			if ports[i].Port == n {
				return n, strValue(ports[i].AppProtocol), true
			}
		}
		return n, "", true
	}
	if want != "" {
		for i := range ports {
			if ports[i].Name == want {
				return ports[i].Port, strValue(ports[i].AppProtocol), true
			}
		}
		return 0, "", false
	}
	if len(ports) == 1 {
		return ports[0].Port, strValue(ports[0].AppProtocol), true
	}
	return 0, "", false
}

// resolvePodPort selects the member port from a Pod's declared container
// ports: numeric as-is, named by container-port name, or the sole declared
// container port
func resolvePodPort(pod *corev1.Pod, want string) (int32, bool) {
	if n, ok := portNumber(want); ok {
		return n, true
	}
	var found int32
	var count int
	for i := range pod.Spec.Containers {
		for _, p := range pod.Spec.Containers[i].Ports {
			if want != "" {
				if p.Name == want {
					return p.ContainerPort, true
				}
				continue
			}
			found = p.ContainerPort
			count++
		}
	}
	if want == "" && count == 1 {
		return found, true
	}
	return 0, false
}

// resolveScheme selects the member scheme: an explicit query scheme wins;
// else a declared https appProtocol; else the well-known
// trickster.io/scheme annotation on the watched object; else http
func resolveScheme(qScheme, appProtocol string, annotations map[string]string) string {
	if qScheme != "" {
		return qScheme
	}
	if appProtocol == "https" {
		return "https"
	}
	if v, ok := annotations[AnnotationScheme]; ok &&
		(v == "http" || v == "https") {
		return v
	}
	return "http"
}

func portNumber(s string) (int32, bool) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return int32(n), true
}

func strValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func joinHostPort(host string, port int32) string {
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// warnPort logs an unresolvable-port condition once per subscription until
// a rebuild resolves cleanly, so churn does not spam the log
func (s *subscription) warnPort(kind, objName string, declared int) {
	s.mtx.Lock()
	warned := s.portWarned
	s.portWarned = true
	s.mtx.Unlock()
	if warned {
		return
	}
	discovery.LogWarn("kubernetes discovery could not resolve a member port; objects skipped",
		logging.Pairs{
			"discoverer": s.p.name, "kind": kind, "object": objName,
			"queryPort": s.q.Port, "declaredPorts": declared,
			"hint": "set query.port to a port name or number",
		})
}

func (s *subscription) clearPortWarn() {
	s.mtx.Lock()
	s.portWarned = false
	s.mtx.Unlock()
}

func (s *subscription) warnBuild(event string, err error) {
	metrics.DiscoveryRefreshErrors.WithLabelValues(
		s.p.name, providers.Kubernetes).Inc()
	discovery.LogWarn(event, logging.Pairs{
		"discoverer": s.p.name, "error": err.Error(),
	})
}
