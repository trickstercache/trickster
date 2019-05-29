# PromSim - a barebones Prometheus data simulator

PromSim is a golang package available at `github.com/Comcast/trickster/pkg/promsim` that facilitates unit testing of components that are direct consumers of Prometheus JSON data. It works by simulating datasets, output in the Prometheus's v1 HTTP API format, that consist of values repeatably generated from the provided query and timerange inputs. The data output by PromSim does not represent reality in any way, and is only useful for unit testing and integration testing, by providing a synthesized Prometheus environment that outputs meaningless data. None of PromSim's result sets are stored on or retrieved from disk, and are calculated just-in-time on every request, using simple mathematical computations.

## Supported Simulation Endpoints

- `/query` (Instantaneous)
- `/query_range` (Time Series)


## Example Usage

```go
package mypackage

import (
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/Comcast/trickster/pkg/promsim"
)

func TestPromSim(t *testing.T) {

	ts := promsim.NewTestServer()
	client := &http.Client{}
	const expected = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"random_label":"57","series_count":"1","series_id":"0"},"values":[[2,"58"]]}]}}`

	resp, err := client.Get(ts.URL + "/api/v1/query_range?query=my_test_query{random_label=57,series_count=1}&start=2&end=2&step=15")
	if err != nil {
		t.Error(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(body) != expected {
		t.Errorf("expected [%s] got [%s]", expected, string(body))
	}
}
```

## Customization

PromSim can be customized in several ways to produce a desired behavior, by providing specific query label values as part of your test queries. All customiztaion labels are optional, and can be used together in any possible combination without issue.

### Series Count

By default, PromSim will only return a single series in the result set. You can provide a label of `series_count` to indicate the exact number of series that should be returned.

Example query that returns 3 series: `query=my_test_query{series_count=3}&start=2&end=2&step=15`

### Latency

PromSim is capable of simulating latency by accepting 2 optional query labels: `latency_ms` and `range_latency_ms`. Both labels can be used in conjunction to produce a desired effect.

#### Upfront Latency

The `latency_ms` label introduces an upfront static processing latency of the provided duration on each http response. This is useful in simulating roundtrip wire latency.

Example adding 300ms of upfront latency: `query=my_test_query{latency_ms=300}&start=2&end=2&step=15`

#### Range Latency

The `range_latency_ms` label produces a per-unique-value latency effect. The result is that the response from PromSim will be delayed by a certain amount, depending upon on the number of series, size of desired timerange and step value. This is useful in simulating very broad label scopes that slow down query response times in the real world.

Example adding 5ms of range latency: `query=my_test_query{range_latency_ms=5,series_count=2}&start=0&end=1800&step=15`. In this example, 1.2s of total latency is introduced (120 datapoints * 2 series * 5ms) into the HTTP response.

### Min and Max Values

The `min_value` and `max_value` labels allow you to define the extent of possible values returned by PromSim in the result set, and are fairly straightforward. The default min and max values, when not customized, are 0 and 100, respectively.

Example of min and max: `query=my_test_query{series_count=2,min_value=32,max_value=212}&start=0&end=90&step=15`. In this case, the returned values will be between 32 and 212, rather than 0 and 100.

### Status Code

The `status_code` label will cause PromSim to return the provided status code instead of `200 OK`. This is useful for testing simulated failcases such as invalid query parameters.

Example query that returns 400 Bad Request: `query=my_test_query{status_code=400}&start=2&end=2&step=15`

### Invalid Reponse Body

The `invalid_response_body` label, when provided and set to a value other than 0, will cause PromSim to return a response that cannot be deserialized into a Prometheus Matrix or Vector object, which is again useful for testing failure handling within your app.

Example query that returns invalid response: `query=my_test_query{invalid_response_body=1}&start=2&end=2&step=15`
