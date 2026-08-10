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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	proxyheaders "github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const (
	dataSourceResponseLimit  = 4 * 1024 * 1024
	metadataRequestTimeout   = 10 * time.Second
	dataSourceCacheTTL       = 5 * time.Minute
	maxDataSourceCacheKeys   = 4096
	maxDataSourceDispatchers = 4096
	grafanaOrgHeader         = "X-Grafana-Org-Id"
	grafanaAuthProxyHeader   = "X-WEBAUTH-USER"
	grafanaJWTHeader         = "X-JWT-Assertion"
)

var errInvalidDataSourceResponse = errors.New("invalid Grafana data source response")

var grafanaDiscoveryIdentityHeaders = []string{
	proxyheaders.NameAuthorization,
	"Cookie",
	grafanaOrgHeader,
	grafanaAuthProxyHeader,
	grafanaJWTHeader,
}

var grafanaCacheIdentityHeaders = slices.Clone(grafanaDiscoveryIdentityHeaders)

type dataSource struct {
	ID        int64          `json:"id"`
	UID       string         `json:"uid"`
	OrgID     int64          `json:"orgId"`
	Type      string         `json:"type"`
	Access    string         `json:"access"`
	URL       string         `json:"url"`
	User      string         `json:"user"`
	Database  string         `json:"database"`
	BasicAuth bool           `json:"basicAuth"`
	JSONData  map[string]any `json:"jsonData"`
}

type dataSourceCacheEntry struct {
	dataSource *dataSource
	expiresAt  time.Time
}

func (c *Client) startPreload() {
	c.preloadOnce.Do(func() {
		headers := c.discoveryHeaders(nil)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), c.metadataTimeout())
			defer cancel()
			if err := c.preloadDataSources(ctx, headers); err != nil {
				logger.Debug("Grafana data source preload unavailable",
					logging.Pairs{"backendName": c.Name(), "detail": err.Error()})
			}
		}()
	})
}

func (c *Client) metadataTimeout() time.Duration {
	d := metadataRequestTimeout
	if o := c.Configuration(); o != nil && o.Timeout > 0 && time.Duration(o.Timeout) < d {
		d = time.Duration(o.Timeout)
	}
	return d
}

func (c *Client) preloadDataSources(ctx context.Context, headers http.Header) error {
	dataSources, err := c.listDataSources(ctx, headers)
	if err != nil {
		return err
	}
	identity := discoveryIdentity(headers)
	stored := 0
	for _, ds := range dataSources {
		if validDataSource(ds) {
			c.storeDataSource(identity, ds)
			stored++
			if stored >= maxDataSourceCacheKeys/2 {
				break
			}
		}
	}
	return nil
}

func (c *Client) resolveDataSource(ctx context.Context, ref dataSourceRef,
	headers http.Header,
) (*dataSource, error) {
	identity := discoveryIdentity(headers)
	key := dataSourceKey(identity, ref.kind, ref.value)
	if ds := c.loadDataSource(key); ds != nil {
		return ds, nil
	}

	v, err, _ := c.lookupGroup.Do(key, func() (any, error) {
		if ds := c.loadDataSource(key); ds != nil {
			return ds, nil
		}

		var ds *dataSource
		if ref.kind == dataSourceRefUID {
			ds = &dataSource{}
			if err := c.getJSON(ctx, "/api/datasources/uid/"+ref.value, headers, ds); err != nil {
				return nil, err
			}
		} else {
			dataSources, err := c.listDataSources(ctx, headers)
			if err != nil {
				return nil, err
			}
			for _, candidate := range dataSources {
				if validDataSource(candidate) && ref.matches(candidate) {
					ds = candidate
					break
				}
			}
		}
		if !validDataSource(ds) || !ref.matches(ds) {
			return nil, errInvalidDataSourceResponse
		}
		c.storeDataSource(identity, ds)
		return ds, nil
	})
	if err != nil {
		return nil, err
	}
	ds, ok := v.(*dataSource)
	if !ok || ds == nil {
		return nil, errInvalidDataSourceResponse
	}
	return ds, nil
}

func (c *Client) listDataSources(ctx context.Context, headers http.Header) ([]*dataSource, error) {
	var dataSources []*dataSource
	if err := c.getJSON(ctx, "/api/datasources", headers, &dataSources); err != nil {
		return nil, err
	}
	return dataSources, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, headers http.Header, out any) error {
	base := c.BaseUpstreamURL()
	if base == nil || base.Scheme == "" || base.Host == "" {
		return errors.New("grafana origin URL is not configured")
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + endpoint
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if host := req.Header.Get(proxyheaders.NameHost); host != "" {
		req.Host = host
		req.Header.Del(proxyheaders.NameHost)
	}
	req.Header.Set("Accept", "application/json")

	client := c.HTTPClient()
	if client == nil {
		return errors.New("grafana HTTP client is not configured")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("grafana data source API returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dataSourceResponseLimit+1))
	if err != nil {
		return err
	}
	if len(body) > dataSourceResponseLimit {
		return errors.New("grafana data source response exceeds limit")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode Grafana data source response: %w", err)
	}
	return nil
}

func (c *Client) discoveryHeaders(r *http.Request) http.Header {
	out := make(http.Header)
	var pathConfigHeaders map[string]string
	identityHeaders := slices.Clone(grafanaDiscoveryIdentityHeaders)
	if r != nil {
		if rsc := request.GetResources(r); rsc != nil && rsc.PathConfig != nil {
			pathConfigHeaders = rsc.PathConfig.RequestHeaders
			identityHeaders = appendUnique(identityHeaders, rsc.PathConfig.CacheKeyHeaders...)
		}
		for _, name := range identityHeaders {
			if name == "" || name == "*" {
				continue
			}
			for _, value := range r.Header.Values(name) {
				out.Add(name, value)
			}
		}
	}

	if r == nil {
		if o := c.Configuration(); o != nil {
			for _, p := range o.Paths {
				if p != nil && p.Path == "/" {
					pathConfigHeaders = p.RequestHeaders
					break
				}
			}
		}
	}
	proxyheaders.UpdateHeaders(out, pathConfigHeaders)
	return out
}

func discoveryIdentity(headers http.Header) string {
	if len(headers) == 0 {
		return "anonymous"
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		if strings.EqualFold(name, "Accept") || strings.EqualFold(name, "Content-Type") {
			continue
		}
		names = append(names, http.CanonicalHeaderKey(name))
	}
	slices.Sort(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(strings.ToLower(name)))
		h.Write([]byte{0})
		values := slices.Clone(headers.Values(name))
		slices.Sort(values)
		for _, value := range values {
			h.Write([]byte(value))
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validDataSource(ds *dataSource) bool {
	return ds != nil && ds.ID > 0 && strings.TrimSpace(ds.Type) != ""
}

func dataSourceKey(identity string, kind dataSourceRefKind, value string) string {
	return identity + "|" + string(kind) + "|" + value
}

func (c *Client) loadDataSource(key string) *dataSource {
	c.mu.Lock()
	entry, ok := c.dataSources[key]
	if ok && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.dataSource
	}
	if ok {
		delete(c.dataSources, key)
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) storeDataSource(identity string, ds *dataSource) {
	if !validDataSource(ds) {
		return
	}
	entry := dataSourceCacheEntry{dataSource: ds, expiresAt: time.Now().Add(dataSourceCacheTTL)}
	c.mu.Lock()
	c.storeDataSourceKey(dataSourceKey(identity, dataSourceRefID,
		strconv.FormatInt(ds.ID, 10)), entry)
	if ds.UID != "" {
		c.storeDataSourceKey(dataSourceKey(identity, dataSourceRefUID, ds.UID), entry)
	}
	c.mu.Unlock()
}

func (c *Client) storeDataSourceKey(key string, entry dataSourceCacheEntry) {
	if _, ok := c.dataSources[key]; !ok && len(c.dataSources) >= maxDataSourceCacheKeys {
		now := time.Now()
		for existingKey, existing := range c.dataSources {
			if !now.Before(existing.expiresAt) {
				delete(c.dataSources, existingKey)
			}
		}
		if len(c.dataSources) >= maxDataSourceCacheKeys {
			for existingKey := range c.dataSources {
				delete(c.dataSources, existingKey)
				break
			}
		}
	}
	c.dataSources[key] = entry
}
