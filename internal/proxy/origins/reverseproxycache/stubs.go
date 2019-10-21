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

package reverseproxycache

import (
	"net/url"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
)

// This file holds funcs required by the Proxy Client or Timeseries interfaces,
// but are (currently) unused by the InfluxDB implementation.

// Series (timeseries.Timeseries Interface) stub funcs

// FastForwardURL is not used for InfluxDB and is here to conform to the Proxy Client interface
func (c Client) FastForwardURL(r *model.Request) (*url.URL, error) {
	return nil, nil
}

// UnmarshalInstantaneous is not used for InfluxDB and is here to conform to the Proxy Client interface
func (c Client) UnmarshalInstantaneous(data []byte) (timeseries.Timeseries, error) {
	return nil, nil
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func (c *Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	return nil, nil
}

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func (c *Client) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	return nil, nil
}

func (c *Client) ParseTimeRangeQuery(r *model.Request) (*timeseries.TimeRangeQuery, error) {
	return nil, nil
}

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *model.Request, extent *timeseries.Extent) {}
