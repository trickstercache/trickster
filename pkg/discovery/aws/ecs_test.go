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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

// awsvpcTask builds a task in the network mode this provider maps: its own
// ENI, and therefore its own routable address.
func awsvpcTask(id, status, health, ip string, tags map[string]string) ecsTask {
	t := ecsTask{
		TaskARN:           "arn:aws:ecs:us-east-1:123456789012:task/prod/" + id,
		ClusterARN:        "arn:aws:ecs:us-east-1:123456789012:cluster/prod",
		TaskDefinitionARN: "arn:aws:ecs:us-east-1:123456789012:task-definition/web:3",
		Group:             "service:web",
		LastStatus:        status,
		DesiredStatus:     taskRunning,
		HealthStatus:      health,
		LaunchType:        "FARGATE",
		AvailabilityZone:  "us-east-1a",
	}
	if ip != "" {
		t.Attachments = []ecsAttachment{{
			Type:   eniAttachmentType,
			Status: "ATTACHED",
			Details: []ecsAttachmentKV{
				{Name: "networkInterfaceId", Value: "eni-1"},
				{Name: eniPrivateIPDetail, Value: ip},
			},
		}}
	}
	for k, v := range tags {
		t.Tags = append(t.Tags, ecsTag{Key: k, Value: v})
	}
	return t
}

// fakeECS serves the AWS JSON 1.1 operations this provider calls, keyed by
// the X-Amz-Target header the protocol uses to name them.
type fakeECS struct {
	*httptest.Server
	mtx             sync.Mutex
	tasks           []ecsTask
	failures        []ecsFailure
	status          int
	errBody         string
	listCalls       int
	descCalls       int
	lastList        map[string]any
	lastDesc        map[string]any
	describeBatches [][]string
}

func newFakeECS(t *testing.T, tasks ...ecsTask) *fakeECS {
	t.Helper()
	f := &fakeECS{tasks: tasks, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("request was not signed")
		}
		require.Equal(t, ecsContentType, r.Header.Get("Content-Type"))
		target := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), ecsTargetPrefix)

		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		f.mtx.Lock()
		defer f.mtx.Unlock()
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			w.Write([]byte(f.errBody))
			return
		}
		switch target {
		case "ListTasks":
			f.listCalls++
			f.lastList = req
			arns := make([]string, 0, len(f.tasks))
			for _, task := range f.tasks {
				arns = append(arns, task.TaskARN)
			}
			// honor the page size the client asked for
			size := len(arns)
			if v, ok := req["maxResults"].(float64); ok && int(v) < size {
				size = int(v)
			}
			start := 0
			if tok, ok := req["nextToken"].(string); ok {
				fmt.Sscanf(tok, "at-%d", &start)
			}
			end := min(start+size, len(arns))
			resp := listTasksResponse{TaskARNs: arns[start:end]}
			if end < len(arns) {
				resp.NextToken = fmt.Sprintf("at-%d", end)
			}
			json.NewEncoder(w).Encode(resp)
		case "DescribeTasks":
			f.descCalls++
			f.lastDesc = req
			var want []string
			for _, a := range req["tasks"].([]any) {
				want = append(want, a.(string))
			}
			f.describeBatches = append(f.describeBatches, want)
			var out describeTasksResponse
			for _, task := range f.tasks {
				for _, a := range want {
					if a == task.TaskARN {
						out.Tasks = append(out.Tasks, task)
					}
				}
			}
			out.Failures = f.failures
			json.NewEncoder(w).Encode(out)
		default:
			t.Errorf("unexpected ECS operation %q", target)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeECS) setError(status int, body string) {
	f.mtx.Lock()
	f.status, f.errBody = status, body
	f.mtx.Unlock()
}

func ecsOptions(endpoint string) *do.Options {
	o := fakeOptions(endpoint)
	o.AWS.Service = awsopts.ServiceECS
	return o
}

func ecsListerFor(t *testing.T, endpoint string, q *do.Query) *ecsLister {
	t.Helper()
	p, err := newProvider("test-aws", ecsOptions(endpoint))
	require.NoError(t, err)
	runner, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription).lister.(*ecsLister)
}

// The whole reason this service exists: Fargate runs awsvpc, which the ec2
// service structurally cannot see.
func TestECSDiscoversAwsvpcTasks(t *testing.T) {
	f := newFakeECS(t,
		awsvpcTask("t1", taskRunning, healthHealthy, "10.0.1.5",
			map[string]string{"trickster-port": "8080", "Name": "web-1"}),
		awsvpcTask("t2", taskRunning, healthHealthy, "10.0.1.6",
			map[string]string{"trickster-port": "8080"}),
	)
	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "prod", Service: "web",
		PortLabel: "trickster-port"})
	snap, skipped, err := l.Members(t.Context())
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, snap, 2)
	require.Equal(t, "10.0.1.5:8080", snap[0].Address)
	require.Equal(t, "web-1", snap[0].Name, "the Name tag becomes the member name")
	require.Equal(t, "t2", snap[1].Name, "otherwise the task id does")
	require.Equal(t, discovery.Ready, snap[0].Ready)
	require.Equal(t, "prod", snap[0].Labels["cluster"])
	require.Equal(t, "service:web", snap[0].Labels["group"])
	require.Equal(t, "FARGATE", snap[0].Labels["launch_type"])
	require.Equal(t, "8080", snap[0].Labels["tag_trickster-port"])
}

// Cluster and service scope the ListTasks call server-side, and tags must
// be requested explicitly or port_label has nothing to read.
func TestECSRequestShape(t *testing.T) {
	f := newFakeECS(t, awsvpcTask("t1", taskRunning, healthHealthy, "10.0.1.5",
		map[string]string{"trickster-port": "8080"}))
	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "prod", Service: "web", Port: "9090"})
	_, _, err := l.Members(t.Context())
	require.NoError(t, err)

	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.Equal(t, "prod", f.lastList["cluster"])
	require.Equal(t, "web", f.lastList["serviceName"])
	require.Equal(t, taskRunning, f.lastList["desiredStatus"])
	require.Equal(t, "prod", f.lastDesc["cluster"])
	require.Equal(t, []any{"TAGS"}, f.lastDesc["include"],
		"tags are not returned unless requested, and port_label reads one")
}

// Tasks on their way out are omitted rather than reported unready, so they
// drain from pools before they stop answering.
func TestECSDepartingTasksAreOmitted(t *testing.T) {
	tags := map[string]string{"trickster-port": "8080"}
	tasks := []ecsTask{
		awsvpcTask("run", taskRunning, healthHealthy, "10.0.1.1", tags),
		awsvpcTask("prov", taskProvisioning, "", "10.0.1.2", tags),
		awsvpcTask("pend", taskPending, "", "10.0.1.3", tags),
		awsvpcTask("act", taskActivating, "", "10.0.1.4", tags),
		awsvpcTask("deact", taskDeactivating, healthHealthy, "10.0.1.5", tags),
		awsvpcTask("stopping", taskStopping, healthHealthy, "10.0.1.6", tags),
		awsvpcTask("deprov", taskDeprovisioning, healthHealthy, "10.0.1.7", tags),
		awsvpcTask("stopped", taskStopped, healthHealthy, "10.0.1.8", tags),
	}
	snap, skipped := tasksToMembers(tasks, mapping{
		scheme: "http", portLabel: "trickster-port",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 4, "running plus the three pre-running states")
	for _, m := range snap {
		if m.Name == "run" {
			require.Equal(t, discovery.Ready, m.Ready)
			continue
		}
		require.Equal(t, discovery.NotReady, m.Ready,
			"a task that is not yet running is a member, but not a ready one")
	}
}

// Health is only reported when the task definition declares a check.
// Treating UNKNOWN as unready would make every un-instrumented task
// permanently unusable.
func TestECSHealthMapping(t *testing.T) {
	tests := map[string]struct {
		status, health string
		want           discovery.ReadyState
	}{
		"running and healthy":      {taskRunning, healthHealthy, discovery.Ready},
		"running, health unknown":  {taskRunning, healthUnknown, discovery.Ready},
		"running, no health check": {taskRunning, "", discovery.Ready},
		"running but unhealthy":    {taskRunning, healthUnhealthy, discovery.NotReady},
		"not yet running":          {taskPending, healthHealthy, discovery.NotReady},
		"unrecognized health":      {taskRunning, "WEIRD", discovery.NotReady},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			task := awsvpcTask("t", test.status, test.health, "10.0.1.1", nil)
			require.Equal(t, test.want, readyForTask(&task))
		})
	}
}

// Only awsvpc yields a task-owned address. Under bridge or host networking
// the address belongs to the container instance, which this provider does
// not resolve -- so the task is excluded with a reason an operator can act
// on, rather than silently missing.
func TestECSNonAwsvpcTaskIsExcludedWithAReason(t *testing.T) {
	bridge := awsvpcTask("t1", taskRunning, healthHealthy, "",
		map[string]string{"trickster-port": "8080"})
	bridge.LaunchType = "EC2"
	bridge.ContainerInstance = "arn:aws:ecs:us-east-1:123456789012:container-instance/prod/ci-1"

	snap, skipped := tasksToMembers([]ecsTask{bridge},
		mapping{scheme: "http", portLabel: "trickster-port"})
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Equal(t, "t1", skipped[0].instanceID)
	require.Contains(t, skipped[0].reason, "awsvpc")
}

// The container's own network interface is the fallback when the
// attachment detail is absent.
func TestECSAddressFallsBackToContainerInterface(t *testing.T) {
	task := awsvpcTask("t1", taskRunning, healthHealthy, "", nil)
	task.Containers = []ecsContainer{{
		Name:              "app",
		NetworkInterfaces: []ecsNetworkInterface{{PrivateIPv4Address: "10.0.9.9"}},
	}}
	require.Equal(t, "10.0.9.9", task.address())
}

func TestECSPortResolution(t *testing.T) {
	// the task tag wins, the static port is the fallback
	tagged := awsvpcTask("t1", taskRunning, healthHealthy, "10.0.1.1",
		map[string]string{"trickster-port": "8080"})
	untagged := awsvpcTask("t2", taskRunning, healthHealthy, "10.0.1.2", nil)
	snap, skipped := tasksToMembers([]ecsTask{tagged, untagged},
		mapping{scheme: "http", portLabel: "trickster-port", port: "9090"})
	require.Empty(t, skipped)
	require.Equal(t, "10.0.1.1:8080", snap[0].Address)
	require.Equal(t, "10.0.1.2:9090", snap[1].Address)

	// with neither, the task is excluded rather than failing the refresh
	snap, skipped = tasksToMembers([]ecsTask{untagged},
		mapping{scheme: "http", portLabel: "trickster-port"})
	require.Empty(t, snap)
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "no port")

	// a nonsense port value is excluded too
	bad := awsvpcTask("t3", taskRunning, healthHealthy, "10.0.1.3",
		map[string]string{"trickster-port": "http"})
	_, skipped = tasksToMembers([]ecsTask{bad},
		mapping{scheme: "http", portLabel: "trickster-port"})
	require.Len(t, skipped, 1)
	require.Contains(t, skipped[0].reason, "not a port number")
}

// DescribeTasks accepts at most 100 ARNs, so a larger cluster must be
// described in batches -- and every batch's tasks must reach the snapshot.
func TestECSDescribeTasksBatches(t *testing.T) {
	var tasks []ecsTask
	for i := range 250 {
		tasks = append(tasks, awsvpcTask(fmt.Sprintf("t%03d", i),
			taskRunning, healthHealthy, fmt.Sprintf("10.0.%d.%d", i/256, i%256),
			map[string]string{"trickster-port": "8080"}))
	}
	f := newFakeECS(t, tasks...)
	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "prod", PortLabel: "trickster-port"})
	snap, skipped, err := l.Members(t.Context())
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, snap, 250)

	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.Equal(t, 3, f.descCalls, "250 tasks is three batches of at most 100")
	for _, b := range f.describeBatches {
		require.LessOrEqual(t, len(b), describeTasksBatch)
	}
}

// ListTasks pages, and a partial page set would be a partial membership.
func TestECSListTasksPaginates(t *testing.T) {
	var tasks []ecsTask
	for i := range 12 {
		tasks = append(tasks, awsvpcTask(fmt.Sprintf("t%02d", i),
			taskRunning, healthHealthy, fmt.Sprintf("10.0.0.%d", i),
			map[string]string{"trickster-port": "8080"}))
	}
	f := newFakeECS(t, tasks...)
	p, err := newProvider("test-aws", ecsOptions(f.URL))
	require.NoError(t, err)
	p.pageSize = 5
	runner, err := p.newSubscription(
		&do.Query{Cluster: "prod", PortLabel: "trickster-port"},
		func(discovery.Snapshot) {})
	require.NoError(t, err)
	snap, _, err := runner.(*subscription).lister.Members(t.Context())
	require.NoError(t, err)
	require.Len(t, snap, 12)
	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.Equal(t, 3, f.listCalls, "12 tasks at 5 per page is three calls")
}

// A task that vanished between the list and the describe is ordinary churn,
// but reporting it beats silently shrinking the pool.
func TestECSDescribeFailuresAreReported(t *testing.T) {
	f := newFakeECS(t, awsvpcTask("t1", taskRunning, healthHealthy, "10.0.1.1",
		map[string]string{"trickster-port": "8080"}))
	f.mtx.Lock()
	f.failures = []ecsFailure{{
		ARN:    "arn:aws:ecs:us-east-1:123456789012:task/prod/gone",
		Reason: "MISSING",
	}}
	f.mtx.Unlock()

	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "prod", PortLabel: "trickster-port"})
	snap, skipped, err := l.Members(t.Context())
	require.NoError(t, err)
	require.Len(t, snap, 1, "the surviving task still becomes a member")
	require.Len(t, skipped, 1)
	require.Equal(t, "gone", skipped[0].instanceID)
	require.Contains(t, skipped[0].reason, "MISSING")
}

// An empty cluster is a valid membership, and must not cost a DescribeTasks
// call with no ARNs.
func TestECSEmptyClusterIsAuthoritative(t *testing.T) {
	f := newFakeECS(t)
	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "prod", Port: "9090"})
	snap, skipped, err := l.Members(t.Context())
	require.NoError(t, err)
	require.Empty(t, snap)
	require.Empty(t, skipped)
	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.Zero(t, f.descCalls)
}

// The JSON error document names the failure far more usefully than the
// status code alone.
func TestECSAPIErrorMessageIsSurfaced(t *testing.T) {
	f := newFakeECS(t)
	f.setError(http.StatusBadRequest,
		`{"__type":"com.amazon.coral.service#ClusterNotFoundException",`+
			`"message":"Cluster not found."}`)
	l := ecsListerFor(t, f.URL, &do.Query{Cluster: "nope", Port: "9090"})
	_, _, err := l.Members(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ClusterNotFoundException")
	require.Contains(t, err.Error(), "Cluster not found")

	f.setError(http.StatusBadGateway, "not json")
	_, _, err = l.Members(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
}

func TestJSONAPIErrorCapitalMessage(t *testing.T) {
	err := jsonAPIError(http.StatusForbidden,
		[]byte(`{"__type":"AccessDeniedException","Message":"nope"}`))
	require.Contains(t, err.Error(), "AccessDeniedException")
	require.Contains(t, err.Error(), "nope")
}

func TestShortARN(t *testing.T) {
	require.Equal(t, "t1",
		shortARN("arn:aws:ecs:us-east-1:123456789012:task/prod/t1"))
	require.Equal(t, "plain", shortARN("plain"))
}

// The ecs service must not be handed ec2's endpoint, or every call 404s.
func TestECSEndpointIsServiceSpecific(t *testing.T) {
	got, err := resolveEndpoint("", awsopts.ServiceECS, "eu-west-2")
	require.NoError(t, err)
	require.Equal(t, "https://ecs.eu-west-2.amazonaws.com", got)
}

// End to end through the poll loop, proving the service dispatch reaches
// the ECS lister.
func TestECSThroughThePollLoop(t *testing.T) {
	f := newFakeECS(t, awsvpcTask("t1", taskRunning, healthHealthy, "10.0.1.1",
		map[string]string{"trickster-port": "8080"}))
	o := ecsOptions(f.URL)
	o.HTTP.Interval = timeconv.Duration(25 * time.Millisecond)
	d, err := New("test-aws", o)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	got := make(chan discovery.Snapshot, 4)
	unsub, err := d.Subscribe(&do.Query{Cluster: "prod", PortLabel: "trickster-port"},
		func(s discovery.Snapshot) { got <- s })
	require.NoError(t, err)
	defer unsub()

	select {
	case snap := <-got:
		require.Len(t, snap, 1)
		require.Equal(t, "10.0.1.1:8080", snap[0].Address)
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot from the ecs service")
	}
}

// realTasksResponse is an actual DescribeTasks document, captured from the
// ECS API against three running Fargate tasks and redacted only for the
// account id.
//
// It settles a question the documentation does not: whether Fargate
// populates containers[].networkBindings. It does not -- the array is
// empty -- which is why this provider takes the port from the query's port
// or port_label rather than deriving it. Deriving would have yielded
// nothing for exactly the launch type this service exists to support.
func realTasksResponse(t *testing.T) *describeTasksResponse {
	t.Helper()
	b, err := os.ReadFile("testdata/describe_tasks.json")
	require.NoError(t, err)
	var out describeTasksResponse
	require.NoError(t, json.Unmarshal(b, &out))
	return &out
}

func TestParseRealDescribeTasksResponse(t *testing.T) {
	resp := realTasksResponse(t)
	require.Len(t, resp.Tasks, 3)
	require.Empty(t, resp.Failures)

	for _, task := range resp.Tasks {
		require.NotEmpty(t, task.TaskARN)
		require.Equal(t, taskRunning, task.LastStatus)
		require.Equal(t, "FARGATE", task.LaunchType)
		require.Equal(t, "service:trickster-sd", task.Group)
		require.NotEmpty(t, task.AvailabilityZone)
		require.NotEmpty(t, task.ClusterARN)

		// the awsvpc address, which is the whole basis of ECS discovery here
		require.NotEmpty(t, task.address(),
			"attachments>details>privateIPv4Address did not decode")

		// tags only arrive because the service was created with
		// --propagate-tags SERVICE and DescribeTasks was called with
		// include: [TAGS]; both are silent when missing
		require.Equal(t, "9090", task.tag("trickster-port"))
		require.Equal(t, "prometheus", task.tag("service"))
		require.Equal(t, "trickster-sd", task.tag("Name"))

		// Fargate reports no host port bindings, which is why the port
		// comes from configuration rather than from the API
		for _, c := range task.Containers {
			require.Empty(t, c.NetworkInterfaces[0].PrivateIPv4Address == "",
				"the container network interface should carry the address too")
		}
	}
}

func TestMapRealTasksToMembers(t *testing.T) {
	resp := realTasksResponse(t)
	snap, skipped := tasksToMembers(resp.Tasks, mapping{
		scheme: "http", portLabel: "trickster-port",
	})
	require.Empty(t, skipped)
	require.Len(t, snap, 3)
	for _, m := range snap {
		require.Regexp(t, `^172\.31\.\d+\.\d+:9090$`, m.Address,
			"the awsvpc address and the port tag should compose the member address")
		require.Equal(t, discovery.Ready, m.Ready)
		require.Equal(t, "trickster-sd", m.Name)
		require.Equal(t, "trickster-sd", m.Labels["cluster"])
		require.Equal(t, "FARGATE", m.Labels["launch_type"])
	}
}
