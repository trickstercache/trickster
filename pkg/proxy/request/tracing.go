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

package request

import "go.opentelemetry.io/otel/attribute"

// TracingAttributes returns low-cardinality request resource attributes for spans.
func (r *Resources) TracingAttributes() []attribute.KeyValue {
	if r == nil {
		return nil
	}

	attrs := make([]attribute.KeyValue, 0, 6)
	if r.BackendOptions != nil {
		if r.BackendOptions.Name != "" {
			attrs = append(attrs, attribute.String("backend.name", r.BackendOptions.Name))
		}
		if r.BackendOptions.Provider != "" {
			attrs = append(attrs, attribute.String("backend.provider", r.BackendOptions.Provider))
		}
	}
	if r.CacheConfig != nil {
		if r.CacheConfig.Name != "" {
			attrs = append(attrs, attribute.String("cache.name", r.CacheConfig.Name))
		}
		if r.CacheConfig.Provider != "" {
			attrs = append(attrs, attribute.String("cache.provider", r.CacheConfig.Provider))
		}
	}
	if r.PathConfig != nil {
		if r.PathConfig.Path != "" {
			attrs = append(attrs, attribute.String("router.path", r.PathConfig.Path))
		}
		if r.PathConfig.HandlerName != "" {
			attrs = append(attrs, attribute.String("router.handler", r.PathConfig.HandlerName))
		}
	}
	return attrs
}
