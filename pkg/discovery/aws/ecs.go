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

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	tslices "github.com/trickstercache/trickster/v2/pkg/util/slices"
)

// ECS speaks AWS JSON 1.1 rather than the Query protocol EC2 uses: the
// operation is named in a header and the parameters are a JSON body.
const (
	ecsTargetPrefix = "AmazonEC2ContainerServiceV20141113."
	ecsContentType  = "application/x-amz-json-1.1"
	// describeTasksBatch is the maximum ARNs DescribeTasks accepts per call
	describeTasksBatch = 100
	// listTasksPageSize is the maximum ListTasks returns per page
	listTasksPageSize = 100
)

// ECS task lifecycle states. A task moves forward through these and never
// back, which is what makes the departing set safe to omit.
const (
	taskProvisioning   = "PROVISIONING"
	taskPending        = "PENDING"
	taskActivating     = "ACTIVATING"
	taskRunning        = "RUNNING"
	taskDeactivating   = "DEACTIVATING"
	taskStopping       = "STOPPING"
	taskDeprovisioning = "DEPROVISIONING"
	taskStopped        = "STOPPED"
)

// ECS container health, reported only when the task definition declares a
// health check.
const (
	healthHealthy   = "HEALTHY"
	healthUnhealthy = "UNHEALTHY"
	healthUnknown   = "UNKNOWN"
)

// eniAttachmentType and eniPrivateIPDetail name the attachment carrying an
// awsvpc task's address.
const (
	eniAttachmentType  = "ElasticNetworkInterface"
	eniPrivateIPDetail = "privateIPv4Address"
)

// listTasksResponse is the ListTasks reply.
type listTasksResponse struct {
	TaskARNs  []string `json:"taskArns"`
	NextToken string   `json:"nextToken"`
}

// describeTasksResponse is the DescribeTasks reply. Failures are reported
// alongside successes rather than as an error.
type describeTasksResponse struct {
	Tasks    []ecsTask    `json:"tasks"`
	Failures []ecsFailure `json:"failures"`
}

type ecsFailure struct {
	ARN    string `json:"arn"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type ecsTask struct {
	TaskARN           string          `json:"taskArn"`
	ClusterARN        string          `json:"clusterArn"`
	TaskDefinitionARN string          `json:"taskDefinitionArn"`
	Group             string          `json:"group"`
	LastStatus        string          `json:"lastStatus"`
	DesiredStatus     string          `json:"desiredStatus"`
	HealthStatus      string          `json:"healthStatus"`
	LaunchType        string          `json:"launchType"`
	AvailabilityZone  string          `json:"availabilityZone"`
	ContainerInstance string          `json:"containerInstanceArn"`
	Attachments       []ecsAttachment `json:"attachments"`
	Containers        []ecsContainer  `json:"containers"`
	Tags              []ecsTag        `json:"tags"`
}

type ecsAttachment struct {
	Type    string            `json:"type"`
	Status  string            `json:"status"`
	Details []ecsAttachmentKV `json:"details"`
}

type ecsAttachmentKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ecsContainer struct {
	Name              string                `json:"name"`
	LastStatus        string                `json:"lastStatus"`
	HealthStatus      string                `json:"healthStatus"`
	NetworkInterfaces []ecsNetworkInterface `json:"networkInterfaces"`
}

type ecsNetworkInterface struct {
	PrivateIPv4Address string `json:"privateIpv4Address"`
}

type ecsTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// jsonErrorResponse is the AWS JSON 1.1 error document.
type jsonErrorResponse struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
	// some operations spell it with a capital M
	MessageAlt string `json:"Message"`
}

// jsonAPIError renders an AWS JSON error document.
func jsonAPIError(status int, payload []byte) error {
	var e jsonErrorResponse
	if err := json.Unmarshal(payload, &e); err == nil {
		msg := e.Message
		if msg == "" {
			msg = e.MessageAlt
		}
		if e.Type != "" || msg != "" {
			// __type is a fully qualified name; the last segment is the
			// part an operator recognizes
			kind := e.Type
			if i := strings.LastIndex(kind, "#"); i >= 0 {
				kind = kind[i+1:]
			}
			return fmt.Errorf("ECS API error (http %d): %s: %s", status, kind, msg)
		}
	}
	return fmt.Errorf("ECS API returned http %d", status)
}

// tag returns the value of the named task tag.
func (t *ecsTask) tag(key string) string {
	for _, tg := range t.Tags {
		if tg.Key == key {
			return tg.Value
		}
	}
	return ""
}

// address returns the task's awsvpc address, which is carried both on the
// ENI attachment and on each container's network interface. The attachment
// is authoritative; the container copy is the fallback.
func (t *ecsTask) address() string {
	for _, a := range t.Attachments {
		if a.Type != eniAttachmentType {
			continue
		}
		for _, d := range a.Details {
			if d.Name == eniPrivateIPDetail && d.Value != "" {
				return d.Value
			}
		}
	}
	for _, c := range t.Containers {
		for _, ni := range c.NetworkInterfaces {
			if ni.PrivateIPv4Address != "" {
				return ni.PrivateIPv4Address
			}
		}
	}
	return ""
}

// isDepartingTask reports whether a task should be omitted from the
// snapshot rather than reported not-ready, so it drains from pools before
// it stops answering.
func isDepartingTask(status string) bool {
	switch status {
	case taskDeactivating, taskStopping, taskDeprovisioning, taskStopped:
		return true
	default:
		return false
	}
}

// readyForTask maps a task's status and health onto member readiness.
//
// Health is only meaningful when the task definition declares a health
// check; ECS reports UNKNOWN otherwise, which is treated as ready because
// the alternative would make every un-instrumented task permanently
// unusable.
func readyForTask(t *ecsTask) discovery.ReadyState {
	if t.LastStatus != taskRunning {
		return discovery.NotReady
	}
	if t.HealthStatus == healthHealthy || t.HealthStatus == healthUnknown ||
		t.HealthStatus == "" {
		return discovery.Ready
	}
	// UNHEALTHY, and anything a future ECS release adds: not assumed healthy
	return discovery.NotReady
}

// toMember maps one task onto a member, or returns why it cannot.
func (t *ecsTask) toMember(m mapping) (discovery.Member, string) {
	address := t.address()
	if address == "" {
		// the only mode without an ENI address is bridge or host
		// networking, where the address belongs to the container instance
		// rather than the task; see ecsLister's doc comment
		return discovery.Member{}, "no awsvpc address: only the awsvpc network mode is supported"
	}
	port := m.port
	if m.portLabel != "" {
		if v := t.tag(m.portLabel); v != "" {
			port = v
		}
	}
	if port == "" {
		return discovery.Member{}, "no port: tag " + m.portLabel + " is absent and no static port is configured"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return discovery.Member{}, "port " + port + " is not a port number"
	}
	name := t.tag("Name")
	if name == "" {
		name = shortARN(t.TaskARN)
	}
	var replicaGroup string
	if m.replicaGroupLabel != "" {
		replicaGroup = t.tag(m.replicaGroupLabel)
	}
	return discovery.Member{
		Name:         name,
		Scheme:       m.scheme,
		Address:      net.JoinHostPort(address, port),
		ReplicaGroup: replicaGroup,
		Ready:        readyForTask(t),
		Labels:       t.labels(),
	}, ""
}

// labels carries task metadata onto the member for observability. Task tags
// are prefixed so an operator-defined tag cannot shadow a
// Trickster-assigned label.
func (t *ecsTask) labels() map[string]string {
	out := map[string]string{
		"task_arn":          t.TaskARN,
		"task_id":           shortARN(t.TaskARN),
		"cluster":           shortARN(t.ClusterARN),
		"task_definition":   shortARN(t.TaskDefinitionARN),
		"group":             t.Group,
		"launch_type":       t.LaunchType,
		"availability_zone": t.AvailabilityZone,
		"task_status":       t.LastStatus,
		"health_status":     t.HealthStatus,
	}
	for _, tg := range t.Tags {
		if tg.Key != "" {
			out["tag_"+tg.Key] = tg.Value
		}
	}
	for k, v := range out {
		if v == "" {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// shortARN returns the final segment of an ARN, which is the part an
// operator reads.
func shortARN(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// tasksToMembers maps tasks onto members, returning both the members and
// the tasks that could not become one.
func tasksToMembers(tasks []ecsTask, m mapping) (discovery.Snapshot, []excluded) {
	out := make(discovery.Snapshot, 0, len(tasks))
	var skipped []excluded
	for i := range tasks {
		t := &tasks[i]
		if isDepartingTask(t.LastStatus) {
			continue
		}
		if !t.hasAllTags(m.tags) {
			continue
		}
		member, reason := t.toMember(m)
		if reason != "" {
			skipped = append(skipped, excluded{shortARN(t.TaskARN), reason})
			continue
		}
		out = append(out, member)
	}
	return out, skipped
}

// hasAllTags reports whether the task carries every required tag key.
func (t *ecsTask) hasAllTags(required []string) bool {
	for _, key := range required {
		if t.tag(key) == "" {
			return false
		}
	}
	return true
}

// ecsLister discovers ECS tasks via ListTasks and DescribeTasks.
//
// # awsvpc only
//
// Only the awsvpc network mode is mapped. In awsvpc each task has its own
// elastic network interface, so DescribeTasks alone yields a routable
// address -- which is also the only mode Fargate offers, and the default
// for new EC2-launch-type services.
//
// Under bridge or host networking the address belongs to the *container
// instance* rather than the task, so resolving it would mean two further
// calls (DescribeContainerInstances, then EC2 DescribeInstances), a second
// signing service, and broader IAM. Rather than ship that unverified, a
// task with no ENI address is excluded with a reason saying so, which an
// operator can act on. See the plan's Step 11 notes.
type ecsLister struct {
	p       *provider
	q       *do.Query
	mapping mapping
}

// Members implements serviceLister: list the cluster's task ARNs, describe
// them in batches, and map the result.
func (l *ecsLister) Members(ctx context.Context) (discovery.Snapshot, []excluded, error) {
	arns, err := l.listTasks(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(arns) == 0 {
		// an authoritatively empty cluster is a valid membership
		return discovery.Snapshot{}, nil, nil
	}
	tasks, failures, err := l.describeTasks(ctx, arns)
	if err != nil {
		return nil, nil, err
	}
	snap, skipped := tasksToMembers(tasks, l.mapping)
	for _, f := range failures {
		// a task that vanished between the list and the describe is normal
		// churn, not an error, but it is worth reporting rather than
		// silently shrinking the pool
		skipped = append(skipped, excluded{shortARN(f.ARN), "describe failed: " + f.Reason})
	}
	return snap, skipped, nil
}

// listTasks pages through ListTasks, returning every selected task ARN.
//
// Pages are accumulated and applied together: a partial page set is a
// partial membership.
func (l *ecsLister) listTasks(ctx context.Context) ([]string, error) {
	var out []string
	var token string
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf(
				"ListTasks did not terminate after %d pages", maxPages)
		}
		resp, err := l.listTasksPage(ctx, token)
		if err != nil {
			return nil, err
		}
		out = append(out, resp.TaskARNs...)
		if resp.NextToken == "" {
			return out, nil
		}
		token = resp.NextToken
	}
}

// listTasksPage performs one signed ListTasks request.
func (l *ecsLister) listTasksPage(ctx context.Context,
	token string,
) (*listTasksResponse, error) {
	req := map[string]any{
		// only running tasks can serve traffic; asking ECS to filter saves
		// describing tasks that would be omitted anyway
		"desiredStatus": taskRunning,
		"maxResults":    l.pageSize(),
	}
	if l.q.Cluster != "" {
		req["cluster"] = l.q.Cluster
	}
	if l.q.Service != "" {
		req["serviceName"] = l.q.Service
	}
	if token != "" {
		req["nextToken"] = token
	}
	var out listTasksResponse
	if err := l.call(ctx, "ListTasks", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// pageSize returns the ListTasks page size, which tests lower to force real
// pagination.
func (l *ecsLister) pageSize() int {
	if l.p.pageSize > 0 {
		return l.p.pageSize
	}
	return listTasksPageSize
}

// describeTasks describes every ARN, in the batches the API accepts.
func (l *ecsLister) describeTasks(ctx context.Context,
	arns []string,
) ([]ecsTask, []ecsFailure, error) {
	var tasks []ecsTask
	var failures []ecsFailure
	for chunk := range tslices.SlicesChunk(arns, describeTasksBatch) {
		req := map[string]any{
			"tasks": chunk,
			// tags are not returned unless asked for, and port_label reads
			// one
			"include": []string{"TAGS"},
		}
		if l.q.Cluster != "" {
			req["cluster"] = l.q.Cluster
		}
		var out describeTasksResponse
		if err := l.call(ctx, "DescribeTasks", req, &out); err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, out.Tasks...)
		failures = append(failures, out.Failures...)
	}
	return tasks, failures, nil
}

// call performs one signed AWS JSON 1.1 request.
func (l *ecsLister) call(ctx context.Context, operation string,
	req any, out any,
) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	payload, err := l.p.post(ctx, string(body), map[string]string{
		"Content-Type": ecsContentType,
		"X-Amz-Target": ecsTargetPrefix + operation,
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decoding %s response: %w", operation, err)
	}
	return nil
}
