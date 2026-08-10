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

package grafana

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

type dataSourceRefKind string

const (
	dataSourceRefID  dataSourceRefKind = "id"
	dataSourceRefUID dataSourceRefKind = "uid"
)

type dataSourceRef struct {
	kind        dataSourceRefKind
	value       string
	proxyPrefix string
	innerPath   string
}

func (r dataSourceRef) matches(ds *dataSource) bool {
	if ds == nil {
		return false
	}
	if r.kind == dataSourceRefUID {
		return ds.UID == r.value
	}
	id, err := strconv.ParseInt(r.value, 10, 64)
	return err == nil && ds.ID == id
}

type dispatchRoute struct {
	options *po.Options
	handler http.Handler
}

type dataSourceDispatcher struct {
	backend backends.Backend
	options *bo.Options
	routes  []*dispatchRoute
}

type dataSourceDispatcherKey struct {
	dataSource string
	proxyPath  string
	parentPath *po.Options
}

// GrafanaHandler accelerates supported Grafana data source proxy calls and
// transparently proxies every other Grafana request.
func (c *Client) GrafanaHandler(w http.ResponseWriter, r *http.Request) {
	ref, ok := parseDataSourceProxyPath(r.URL.Path)
	if !ok {
		c.ProxyHandler(w, r)
		return
	}

	discoveryHeaders := c.discoveryHeaders(r)
	ds, err := c.resolveDataSource(r.Context(), ref, discoveryHeaders)
	if err != nil {
		logger.Debug("Grafana data source lookup unavailable",
			logging.Pairs{"backendName": c.Name(), "detail": err.Error()})
		c.ProxyHandler(w, r)
		return
	}
	provider, ok := providerForDataSource(ds)
	if !ok {
		c.ProxyHandler(w, r)
		return
	}

	dispatcher, err := c.getDispatcher(ref, ds, provider, configuredPathOptions(r))
	if err != nil {
		logger.Error("could not configure Grafana data source dispatcher",
			logging.Pairs{"backendName": c.Name(), "provider": provider, "detail": err.Error()})
		c.ProxyHandler(w, r)
		return
	}
	route := dispatcher.match(ref.innerPath, r.Method)
	if route == nil {
		c.ProxyHandler(w, r)
		return
	}

	r.URL.Path = ref.innerPath
	r.URL.RawPath = ""
	parentResources := request.GetResources(r)
	h := middleware.LimitQueryRange(route.handler)
	if parentResources == nil {
		h = middleware.WithResourcesContext(dispatcher.backend, dispatcher.options,
			c.Cache(), route.options, nil, h)
		h.ServeHTTP(w, r)
		return
	}
	h = middleware.WithResourcesContext(dispatcher.backend, dispatcher.options,
		c.Cache(), route.options, parentResources.Tracer, h)
	h.ServeHTTP(w, r)
}

func parseDataSourceProxyPath(requestPath string) (dataSourceRef, bool) {
	const prefix = "/api/datasources/proxy/"
	if !strings.HasPrefix(requestPath, prefix) {
		return dataSourceRef{}, false
	}
	remainder := strings.TrimPrefix(requestPath, prefix)
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 {
		return dataSourceRef{}, false
	}

	ref := dataSourceRef{kind: dataSourceRefID}
	innerIndex := 1
	if parts[0] == "uid" {
		if len(parts) < 2 || parts[1] == "" {
			return dataSourceRef{}, false
		}
		ref.kind = dataSourceRefUID
		ref.value = parts[1]
		innerIndex = 2
	} else {
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 {
			return dataSourceRef{}, false
		}
		ref.value = parts[0]
	}
	if strings.Contains(ref.value, "/") || ref.value == "" {
		return dataSourceRef{}, false
	}

	ref.proxyPrefix = prefix + strings.Join(parts[:innerIndex], "/")
	ref.innerPath = "/"
	if len(parts) > innerIndex {
		ref.innerPath += strings.Join(parts[innerIndex:], "/")
	}
	return ref, true
}

func providerForDataSource(ds *dataSource) (string, bool) {
	if ds == nil || !strings.EqualFold(ds.Access, "proxy") {
		return "", false
	}
	switch strings.ToLower(ds.Type) {
	case providers.Prometheus:
		return providers.Prometheus, true
	case providers.InfluxDB:
		return providers.InfluxDB, true
	case "vertamedia-clickhouse-datasource":
		return providers.ClickHouse, true
	default:
		return "", false
	}
}

func (c *Client) getDispatcher(ref dataSourceRef, ds *dataSource,
	provider string, parentPath *po.Options,
) (*dataSourceDispatcher, error) {
	key := dataSourceDispatcherKey{
		dataSource: dataSourceIdentity(provider, ds),
		proxyPath:  ref.proxyPrefix,
		parentPath: parentPath,
	}
	c.mu.RLock()
	dispatcher := c.dispatchers[key]
	c.mu.RUnlock()
	if dispatcher != nil {
		return dispatcher, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if dispatcher = c.dispatchers[key]; dispatcher != nil {
		return dispatcher, nil
	}
	dispatcher, err := c.newDispatcher(ref, ds, provider, parentPath)
	if err != nil {
		return nil, err
	}
	if len(c.dispatchers) >= maxDataSourceDispatchers {
		for existingKey := range c.dispatchers {
			delete(c.dispatchers, existingKey)
			break
		}
	}
	c.dispatchers[key] = dispatcher
	return dispatcher, nil
}

func (c *Client) newDispatcher(ref dataSourceRef, ds *dataSource, provider string,
	parentPath *po.Options,
) (*dataSourceDispatcher, error) {
	parent := c.Configuration()
	if parent == nil {
		return nil, errors.New("grafana backend options are not configured")
	}
	factory := c.factories[provider]
	if factory == nil {
		return nil, fmt.Errorf("backend provider %q is not registered", provider)
	}

	name, cachePrefix := dataSourceBackendIdentity(c.Name(), provider, ds)
	o := parent.Clone()
	o.Name = name
	o.Provider = provider
	o.ReplicaGroup = ""
	o.Paths = nil
	o.FastForwardPath = nil
	o.HTTPClient = nil
	o.HealthCheck = nil
	o.Hosts = nil
	o.IsDefault = false
	o.PathRoutingDisabled = true
	o.AuthenticatorName = ""
	o.AuthOptions = nil
	o.ReqRewriterName = ""
	o.ReqRewriter = nil
	o.LatencyMin = 0
	o.LatencyMax = 0
	o.CacheKeyPrefix = cachePrefix
	o.OriginURL = dataSourceOriginURL(c.BaseUpstreamURL(), ref.proxyPrefix)
	if err := o.Initialize(name); err != nil {
		return nil, err
	}

	backend, err := factory(name, o, lm.NewRouter(), c.Cache(), c.clients, c.factories)
	if err != nil {
		return nil, err
	}
	// Every dynamic backend still calls Grafana, so share the parent transport
	// instead of retaining one connection pool per discovered data source.
	o.HTTPClient = c.HTTPClient()
	paths := backend.DefaultPathConfigs(o)
	for _, path := range paths {
		configureDataSourcePath(path, parentPath)
	}
	if o.FastForwardPath != nil {
		configureDataSourcePath(o.FastForwardPath, parentPath)
	}
	if err := paths.Initialize(); err != nil {
		return nil, err
	}
	o.Paths = paths

	handlerLookup := backend.Handlers()
	routes := make([]*dispatchRoute, 0, len(paths))
	for _, path := range paths {
		if path == nil {
			continue
		}
		h := handlerLookup[path.HandlerName]
		if h != nil {
			routes = append(routes, &dispatchRoute{options: path, handler: h})
		}
	}
	return &dataSourceDispatcher{backend: backend, options: o, routes: routes}, nil
}

func (d *dataSourceDispatcher) match(requestPath, method string) *dispatchRoute {
	if d == nil {
		return nil
	}
	for _, route := range d.routes {
		if route == nil || route.options == nil || !slices.Contains(route.options.Methods, method) {
			continue
		}
		if route.options.MatchType == matching.PathMatchTypePrefix {
			if strings.HasPrefix(requestPath, route.options.Path) {
				return route
			}
		} else if requestPath == route.options.Path {
			return route
		}
	}
	return nil
}

func configureDataSourcePath(path, parent *po.Options) {
	if path == nil {
		return
	}
	if path.RequestHeaders == nil {
		path.RequestHeaders = make(map[string]string)
	}
	if parent != nil {
		maps.Copy(path.RequestHeaders, parent.RequestHeaders)
		if path.RequestParams == nil {
			path.RequestParams = make(map[string]string)
		}
		maps.Copy(path.RequestParams, parent.RequestParams)
		if path.ResponseHeaders == nil {
			path.ResponseHeaders = make(map[string]string)
		}
		maps.Copy(path.ResponseHeaders, parent.ResponseHeaders)
		if slices.Contains(parent.CacheKeyParams, "*") {
			path.CacheKeyParams = []string{"*"}
		} else {
			path.CacheKeyParams = appendUnique(path.CacheKeyParams, parent.CacheKeyParams...)
		}
		path.CacheKeyHeaders = appendUnique(path.CacheKeyHeaders, parent.CacheKeyHeaders...)
		path.CacheKeyFormFields = appendUnique(path.CacheKeyFormFields, parent.CacheKeyFormFields...)
		path.NoMetrics = parent.NoMetrics
		if parent.CollapsedForwardingName != "" {
			path.CollapsedForwardingName = parent.CollapsedForwardingName
			path.CollapsedForwardingType = parent.CollapsedForwardingType
		}
		if parent.ResponseCode != 0 {
			path.ResponseCode = parent.ResponseCode
		}
		if parent.ResponseBody != nil {
			path.ResponseBody = parent.ResponseBody
			path.ResponseBodyBytes = slices.Clone(parent.ResponseBodyBytes)
		}
	}
	path.CacheKeyHeaders = appendUnique(path.CacheKeyHeaders, grafanaCacheIdentityHeaders...)
}

func configuredPathOptions(r *http.Request) *po.Options {
	if r == nil {
		return nil
	}
	rsc := request.GetResources(r)
	if rsc == nil || rsc.PathConfig == nil {
		return nil
	}
	return rsc.PathConfig
}

func appendUnique(values []string, additions ...string) []string {
	for _, addition := range additions {
		if !slices.ContainsFunc(values, func(value string) bool {
			return strings.EqualFold(value, addition)
		}) {
			values = append(values, addition)
		}
	}
	return values
}

func dataSourceOriginURL(base *url.URL, proxyPrefix string) string {
	if base == nil {
		return ""
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + proxyPrefix
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func dataSourceIdentity(provider string, ds *dataSource) string {
	logicalID := ds.UID
	if logicalID == "" {
		logicalID = strconv.FormatInt(ds.ID, 10)
	}
	identity := struct {
		OrgID     int64          `json:"orgId"`
		LogicalID string         `json:"logicalId"`
		Provider  string         `json:"provider"`
		URL       string         `json:"url"`
		User      string         `json:"user"`
		Database  string         `json:"database"`
		BasicAuth bool           `json:"basicAuth"`
		JSONData  map[string]any `json:"jsonData"`
	}{
		OrgID: ds.OrgID, LogicalID: logicalID, Provider: provider,
		URL: ds.URL, User: ds.User, Database: ds.Database,
		BasicAuth: ds.BasicAuth, JSONData: ds.JSONData,
	}
	b, _ := json.Marshal(identity)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func dataSourceBackendIdentity(parentName, provider string, ds *dataSource) (string, string) {
	suffix := dataSourceIdentity(provider, ds)
	return parentName + "-datasource-" + suffix, parentName + ".grafana." + suffix
}
