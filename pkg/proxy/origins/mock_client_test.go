/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package origins

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/cache"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
)

type TestClient struct {
}

func (c *TestClient) Configuration() *oo.Options {
	return &oo.Options{}
}

func (c *TestClient) DefaultPathConfigs(oc *oo.Options) map[string]*po.Options {
	return nil
}

func (c *TestClient) HTTPClient() *http.Client {
	return nil
}

func (c *TestClient) Handlers() map[string]http.Handler {
	return nil
}

func (c *TestClient) Name() string {
	return "test"
}

func (c *TestClient) Router() http.Handler {
	return http.NewServeMux()
}

func (c *TestClient) SetCache(cc cache.Cache) {}
