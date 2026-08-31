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
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// The ECS live suite needs a cluster running at least three awsvpc tasks
// whose tags include trickster-port. It is skipped unless both
// TRICKSTER_AWS_TEST=1 and TRICKSTER_ECS_CLUSTER are set, so the EC2 live
// tests still run for accounts with no ECS fixture:
//
//	TRICKSTER_AWS_TEST=1 TRICKSTER_AWS_PROFILE=myprofile \
//	  TRICKSTER_ECS_CLUSTER=trickster-sd TRICKSTER_ECS_SERVICE=trickster-sd \
//	  go test ./pkg/discovery/aws/ -run Live -v -count=1
//
// The fixture's service must be created with --propagate-tags SERVICE.
// Without it ECS does not copy service tags onto tasks, and port_label has
// nothing to read -- which is exactly the kind of thing only a live run
// catches.
func liveECSQuery(t *testing.T) *do.Query {
	t.Helper()
	cluster := os.Getenv("TRICKSTER_ECS_CLUSTER")
	if cluster == "" {
		t.Skip("set TRICKSTER_ECS_CLUSTER to run the ECS live tests")
	}
	return &do.Query{
		Cluster:   cluster,
		Service:   os.Getenv("TRICKSTER_ECS_SERVICE"),
		PortLabel: "trickster-port",
	}
}

func liveECSLister(t *testing.T, q *do.Query, pageSize int) *ecsLister {
	t.Helper()
	p, err := newProvider("live-ecs", liveAWSOptions(t, awsopts.ServiceECS))
	require.NoError(t, err)
	p.pageSize = pageSize
	runner, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription).lister.(*ecsLister)
}

// The point of the ECS live suite: AWS JSON 1.1 is a different protocol
// from EC2's Query, so accepting our signature is a separate fact, and the
// two-call ListTasks/DescribeTasks shape is a separate risk.
func TestLiveECSSigningAndResponseShape(t *testing.T) {
	q := liveECSQuery(t)
	l := liveECSLister(t, q, 0)

	arns, err := l.listTasks(t.Context())
	require.NoError(t, err, "AWS rejected ListTasks or the response did not decode")
	require.NotEmpty(t, arns, "no tasks in the fixture cluster")

	tasks, failures, err := l.describeTasks(t.Context(), arns)
	require.NoError(t, err, "AWS rejected DescribeTasks or the response did not decode")
	require.Empty(t, failures)
	require.Len(t, tasks, len(arns))

	for _, task := range tasks {
		require.NotEmpty(t, task.TaskARN)
		require.NotEmpty(t, task.LastStatus)
		require.NotEmpty(t, task.Group)
		require.NotEmpty(t, task.address(),
			"no awsvpc address; is the task definition networkMode awsvpc?")
	}
	t.Logf("described %d tasks", len(tasks))
}

// Task tags only exist if the service was created with
// --propagate-tags SERVICE, and DescribeTasks only returns them when asked.
// Both are easy to get wrong and silent when wrong, so assert them.
func TestLiveECSTagsArePropagatedAndReturned(t *testing.T) {
	q := liveECSQuery(t)
	l := liveECSLister(t, q, 0)
	arns, err := l.listTasks(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, arns)
	tasks, _, err := l.describeTasks(t.Context(), arns)
	require.NoError(t, err)

	for _, task := range tasks {
		require.NotEmpty(t, task.Tags,
			"no task tags: create the service with --propagate-tags SERVICE")
		require.NotEmpty(t, task.tag("trickster-port"),
			"the trickster-port tag did not reach the task")
	}
}

// ListTasks pagination against the real API. maxResults is forced low so a
// three-task fixture produces several pages.
func TestLiveECSPagination(t *testing.T) {
	q := liveECSQuery(t)
	unpaged, err := liveECSLister(t, q, 0).listTasks(t.Context())
	require.NoError(t, err)
	require.Greater(t, len(unpaged), 1,
		"pagination needs more than one task to produce a second page")

	paged, err := liveECSLister(t, q, 1).listTasks(t.Context())
	require.NoError(t, err)
	require.Len(t, paged, len(unpaged),
		"paging lost or duplicated tasks relative to a single-page read")
}

// The end-to-end result: what would actually land in an ALB pool.
func TestLiveECSMembersAreUsable(t *testing.T) {
	q := liveECSQuery(t)
	l := liveECSLister(t, q, 0)
	snap, skipped, err := l.Members(t.Context())
	require.NoError(t, err)
	for _, e := range skipped {
		t.Logf("excluded %s: %s", e.instanceID, e.reason)
	}
	require.NotEmpty(t, snap)
	for _, m := range snap {
		require.NotEmpty(t, m.Address)
		require.Equal(t, discovery.Ready, m.Ready)
	}
	t.Logf("mapped %d members, excluded %d", len(snap), len(skipped))
}
