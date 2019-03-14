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

package influxdb

import (
	"net/http"
	"net/url"

	"encoding/json"
	"fmt"
	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client Implements the Database Client Interface
type Client struct {
	Name   string
	User   string
	Pass   string
	Config config.OriginConfig
	Cache  cache.Cache
}

const (
	APIPath = "/"
	mnQuery = "query"
	health  = "ping"
)

// Common URL Parameter Names
const (
	upQuery = "q"
)

var reType, reTime1, reTime2, reStep, reparentheses, reTime1Parse, reTime2Parse *regexp.Regexp

func init() {
	reType = regexp.MustCompile(`(?i)^(?P<statementType>SELECT|\w+)`)
	reTime1 = regexp.MustCompile(`(?i)(?P<preOp1>where|and)\s+(?P<timeExpr1>time\s+(?P<relationalOp1>>=|>|=)\s+(?P<value1>((?P<ts1>[0-9]+)(?P<tsUnit1>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now1>now\(\))\s+(?P<operand1>[+-])\s+(?P<offset1>[0-9]+[mhsdwy]))))(\s+(?P<postOp1>and|or|group|order|limit)|$)`)
	reTime2 = regexp.MustCompile(`(?i)(?P<preOp2>where|and)\s+(?P<timeExpr2>time\s+(?P<relationalOp2><=|<)\s+(?P<value2>((?P<ts2>[0-9]+)(?P<tsUnit2>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now2>now\(\))\s+(?P<operand2>[+-])\s+(?P<offset2>[0-9]+[mhsdwy]))))(\s+(?P<postOp2>and|or|group|order|limit)|$)`)
	reStep = regexp.MustCompile(`(?i)\s+group\s+by\s+.*time\((?P<step>[0-9]+[mhsdw])\)`)
	reparentheses = regexp.MustCompile(`(?i)\((.*?)\)`)
	reTime1Parse = regexp.MustCompile(`(?i)(?P<relationalOp2>=|>|>=)\s+(?P<value2>((?P<ts2>[0-9]+)(?P<tsUnit2>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now2>now\(\))\s+(?P<operand2>[+-])\s+(?P<offset2>[0-9]+[mhsdwy])))`)
	reTime2Parse = regexp.MustCompile(`(?i)(?P<relationalOp2><=|<)\s+(?P<value2>((?P<ts2>[0-9]+)(?P<tsUnit2>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now2>now\(\))\s+(?P<operand2>[+-])\s+(?P<offset2>[0-9]+[mhsdwy])))`)
}

// Configuration ...
func (c Client) Configuration() config.OriginConfig {
	return c.Config
}

// SetExtent ...
func (c Client) SetExtent(r *proxy.Request, extent *timeseries.Extent) {
	params := r.URL.Query()
	r.URL.RawQuery = params.Encode()
	//Do nothing here, since we are not using the queryUp and queryDown values as query params
}

// CacheInstance ...
func (c Client) CacheInstance() cache.Cache {
	return c.Cache
}

// BaseURL ...
func (c Client) BaseURL() *url.URL {
	u := &url.URL{}
	u.Scheme = c.Config.Scheme
	u.Host = c.Config.Host
	u.Path = c.Config.PathPrefix
	return u
}

// UnmarshalInstantaneous ...
func (c Client) UnmarshalInstantaneous() timeseries.Timeseries {
	return SeriesEnvelope{}
}

// BuildUpstreamURL ...
func (c Client) BuildUpstreamURL(r *http.Request) *url.URL {

	return &url.URL{}
}

// OriginName ...
func (c Client) OriginName() string {
	return c.Name
}

// DeriveCacheKey ...
func (c Client) DeriveCacheKey(path string, params url.Values, prefix string, extra string) string {
	k := path
	// if we have a prefix, set it up
	if len(prefix) > 0 {
		k += prefix
	}

	if p, ok := params[upQuery]; ok {
		k += p[0]
	}

	if len(extra) > 0 {
		k += extra
	}
	return md5.Checksum(k)
}

func (c *Client) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	proxy.ProxyRequest(proxy.NewRequest(c.Name, proxy.OtInfluxDb, "APIProxyHandler", r.Method, c.BuildUpstreamURL(r), r.Header, r), w)
}

func (c *Client) QueryRangeHandler(w http.ResponseWriter, r *http.Request) {
}

func (c Client) QueryHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BuildUpstreamURL(r)
	proxy.ObjectProxyCacheRequest(proxy.NewRequest(c.Name, proxy.OtInfluxDb, "QueryHandler", r.Method, u, r.Header, r), w, &c, c.Cache, 30, false, false)
}

func getTimeValueForQueriesWithoutNow(timeParsed []string) int64 {
	suffix := strings.SplitAfterN(timeParsed[0], " ", 2)
	timeWithoutOperator := strings.TrimSpace(suffix[1])
	timeWithoutOperator = timeWithoutOperator[2:]
	unit := timeWithoutOperator[len(timeWithoutOperator)-1]
	var multiplier = time.Nanosecond

	switch unit {
	case 'u':
	case 'µ':
		multiplier = time.Microsecond
	case 'w':
		multiplier = 24 * 7 * time.Hour
	case 'd':
		multiplier = 24 * time.Hour
	case 'h':
		multiplier = time.Hour
	case 'm':
		multiplier = time.Minute
	case 's':
		multiplier = time.Second
	default:
		if timeWithoutOperator[len(timeWithoutOperator)-2] == 'm' {
			multiplier = time.Millisecond
		} else {
			multiplier = time.Nanosecond
		}
	}
	re := regexp.MustCompile("[0-9]+")
	number := re.FindAllString(timeWithoutOperator, -1)
	numValue, _ := (strconv.ParseInt(number[0], 10, 32))
	timeValue := numValue * multiplier.Nanoseconds()
	return timeValue
}

func getTimeValueForQueriesWithNow(timeParsed []string) (int64, string) {
	suffix := strings.SplitAfterN(timeParsed[0], "now()", 2)
	timeWithOperator := strings.TrimSpace(suffix[1])
	timeWithOperator = timeWithOperator[2:]
	unit := timeWithOperator[len(timeWithOperator)-1]
	var multiplier = time.Nanosecond
	switch unit {
	case 'y':
		multiplier = 365 * 24 * time.Hour
	case 'w':
		multiplier = 24 * 7 * time.Hour
	case 'd':
		multiplier = 24 * time.Hour
	case 'h':
		multiplier = time.Hour
	case 'm':
		multiplier = time.Minute
	case 's':
		multiplier = time.Second
	default:
		multiplier = time.Nanosecond
	}
	num := timeWithOperator[2 : len(timeWithOperator)-1]
	numValue, _ := (strconv.ParseInt(num, 10, 32))
	timeValue := numValue * multiplier.Nanoseconds()
	return timeValue, timeWithOperator
}

// ParseTimeRangeQuery ...
//todo: convert this giant function into a list of smaller functions
func (c Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	qi := r.URL.Query()
	if p, ok := qi[upQuery]; ok {
		trq.Statement = p[0]
	} else {
		return nil, proxy.ErrorMissingURLParam(upQuery)
	}

	stepArray := reStep.FindAllString(trq.Statement, -1)
	if stepArray != nil && len(stepArray) != 0 {
		stepWithParen := reparentheses.FindAllString(stepArray[0], -1)
		if stepWithParen != nil && len(stepWithParen) != 0 {
			step, err := strconv.ParseInt(stepWithParen[0][1:len(stepWithParen)-1], 10, 0)
			if err != nil {
				return nil, proxy.ErrorMissingURLParam(upQuery)
			} else {
				trq.Step = step
			}
		}
	}

	//<= | <
	time2 := reTime2.FindAllString(trq.Statement, -1)
	if time2 != nil && len(time2) != 0 {
		time2Parsed := reTime2Parse.FindAllString(time2[0], -1)
		if time2Parsed != nil && len(time2Parsed) != 0 {

			if strings.Index(time2Parsed[0], "now()") != -1 {
				timeValue, time2WithOperator := getTimeValueForQueriesWithNow(time2Parsed)
				operator := time2WithOperator[0]
				switch operator {
				case '-':
					timeValue = time.Now().UTC().UnixNano() - timeValue
					trq.Extent.Start, _ = time.Parse(time.RFC3339, string(timeValue))
					trq.Extent.End = time.Now().UTC()
				case '+':
					timeValue = time.Now().UnixNano() + timeValue
					trq.Extent.Start = time.Now().UTC()
					trq.Extent.End, _ = time.Parse(time.RFC3339, string(timeValue))
				default:
					timeValue = time.Now().UTC().UnixNano()
				}

			} else {
				trq.Extent.End, _ = time.Parse(time.RFC3339, string(getTimeValueForQueriesWithoutNow(time2Parsed)))
			}
		}

	} else {
		if trq.Extent.End.IsZero() {
			trq.Extent.End = time.Now().UTC()
		}
	}

	// >|=|>=
	time1 := reTime1.FindAllString(trq.Statement, -1)
	if time1 != nil && len(time1) != 0 {
		time1Parsed := reTime1Parse.FindAllString(time1[0], -1)
		if time1Parsed != nil && len(time1Parsed) != 0 {

			if strings.Index(time1Parsed[0], "now()") != -1 {
				timeValue, time1WithOperator := getTimeValueForQueriesWithNow(time1Parsed)
				operator := time1WithOperator[0]
				switch operator {
				case '-':
					timeValue = time.Now().UTC().UnixNano() - timeValue
					trq.Extent.Start, _ = time.Parse(time.RFC3339, string(timeValue))
					if trq.Extent.End.IsZero() {
						trq.Extent.End = time.Now().UTC()
					}
				case '+':
					timeValue = time.Now().UnixNano() + timeValue
					trq.Extent.Start, _ = time.Parse(time.RFC3339, string(timeValue))
					trq.Extent.End, _ = time.Parse(time.RFC3339, string(timeValue))
				default:
					timeValue = time.Now().UnixNano()
				}

			} else {
				trq.Extent.Start, _ = time.Parse(time.RFC3339, string(getTimeValueForQueriesWithoutNow(time1Parsed)))
			}
		}

	} else {
		if trq.Extent.Start.IsZero() {
			trq.Extent.Start = time.Now().UTC()
		}
	}

	return trq, nil
}

// HealthHandler ...
func (c Client) HealthHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BaseURL()
	u.Path += APIPath + health
	proxy.ProxyRequest(proxy.NewRequest(c.Name, proxy.OtInfluxDb, "HealthHandler", http.MethodGet, u, r.Header, r), w)
}

// MarshalTimeseries ...
func (c Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	// Marshal the Envelope back to a json object for Cache Storage
	return json.Marshal(ts)
}

// RegisterRoutes ...
func (c Client) RegisterRoutes(originName string, o config.OriginConfig) {
	fmt.Println("Registering Origin Handlers"+"originType"+o.Type, "originName"+originName)
	routing.Router.HandleFunc("/"+health, c.HealthHandler).Methods("GET")
	routing.Router.HandleFunc("/"+originName+APIPath+mnQuery, c.QueryHandler).Methods("GET")
	routing.Router.PathPrefix("/" + originName + APIPath).HandlerFunc(c.ProxyHandler).Methods("GET")
}
