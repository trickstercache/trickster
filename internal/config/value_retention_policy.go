/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import "strconv"

// ValueRetentionPolicy enumerates polices for maintaining time series cache data
type ValueRetentionPolicy int

const (
	// RetentionPolicySize indicates a that a time series cache object hold up to ValueRetentionFactor number of
	// unique timestamps, removing the oldest timestamps as necessary to to remain at the ValueRetentionFactor
	// This policy is the more performant for times when there are frequent requests for older data at a high
	// resolution, because you can cache older data, but at the expense of querying the cache on every request
	// to determine the count of available timestamps in the cache object to know if the request is cacheable at all
	RetentionPolicySize = ValueRetentionPolicy(iota)
	// RetentionPolicyDate indicates that a time series cache object only holds values newer than an explicit date,
	// called the Oldest Cacheable Timestamp, which is calculated with this formula:
	// time.Now().Add(-(config.ValueRetentionFactor * query.Step))
	// This policy is the more performant for times when there are infrequent requests for older data at a high
	// resolution, because out-of-cache-range determination does not require querying the cache before
	RetentionPolicyDate
)

var valueRetentionPolicyNames = map[string]ValueRetentionPolicy{
	"size": RetentionPolicySize,
	"date": RetentionPolicyDate,
}

func (p ValueRetentionPolicy) String() string {
	name := []string{"size", "date"}
	i := uint8(p)
	switch {
	case i <= uint8(RetentionPolicyDate):
		return name[i]
	default:
		return strconv.Itoa(int(i))
	}
}
