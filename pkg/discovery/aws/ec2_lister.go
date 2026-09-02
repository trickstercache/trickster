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
	"fmt"
	"net/url"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
)

// ec2Lister discovers EC2 instances via DescribeInstances.
type ec2Lister struct {
	p       *provider
	filters map[string][]string
	mapping mapping
}

// Members implements serviceLister.
func (l *ec2Lister) Members(ctx context.Context) (discovery.Snapshot, []excluded, error) {
	instances, err := l.describeInstances(ctx)
	if err != nil {
		return nil, nil, err
	}
	snap, skipped := toMembers(instances, l.mapping)
	return snap, skipped, nil
}

// describeInstances pages through DescribeInstances, returning every
// instance the filters selected.
//
// Pages are accumulated and applied together: a partial page set is a
// partial membership, and emitting one would drain the pool of everything
// the later pages would have contained.
func (l *ec2Lister) describeInstances(ctx context.Context) ([]ec2Instance, error) {
	var out []ec2Instance
	var token string
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf(
				"DescribeInstances did not terminate after %d pages", maxPages)
		}
		resp, err := l.describeInstancesPage(ctx, token)
		if err != nil {
			return nil, err
		}
		for _, r := range resp.Reservations {
			out = append(out, r.Instances...)
		}
		if resp.NextToken == "" {
			return out, nil
		}
		token = resp.NextToken
	}
}

// describeInstancesPage performs one signed DescribeInstances request.
func (l *ec2Lister) describeInstancesPage(ctx context.Context,
	token string,
) (*describeInstancesResponse, error) {
	// the Query protocol carries its parameters as a form-encoded body,
	// which SigV4 hashes as the payload
	payload, err := l.p.post(ctx, l.describeInstancesForm(token).Encode(),
		map[string]string{
			"Content-Type": "application/x-www-form-urlencoded; charset=utf-8",
		})
	if err != nil {
		return nil, err
	}
	return parseDescribeInstances(payload)
}

// describeInstancesForm builds the Query-protocol parameters.
func (l *ec2Lister) describeInstancesForm(token string) url.Values {
	v := url.Values{}
	v.Set("Action", "DescribeInstances")
	v.Set("Version", ec2APIVersion)
	if token != "" {
		v.Set("NextToken", token)
	}
	if l.p.pageSize > 0 {
		v.Set("MaxResults", strconv.Itoa(l.p.pageSize))
	}
	// Filter.N.Name / Filter.N.Value.M, one-indexed. Names are sorted so
	// the request is deterministic, which keeps it comparable in logs and
	// in tests.
	for n, name := range sortedKeys(l.filters) {
		prefix := "Filter." + strconv.Itoa(n+1)
		v.Set(prefix+".Name", name)
		for m, value := range l.filters[name] {
			v.Set(prefix+".Value."+strconv.Itoa(m+1), value)
		}
	}
	return v
}
