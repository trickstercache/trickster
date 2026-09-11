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

package config

import logmanager "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"

// LogManagerOptions returns every configured application, access and error log.
func (c *Config) LogManagerOptions() []*logmanager.Options {
	if c == nil {
		return nil
	}
	var instanceID int
	if c.Main != nil {
		instanceID = c.Main.InstanceID
	}
	options := make([]*logmanager.Options, 0, 1+len(c.Backends)*2)
	appendOption := func(o *logmanager.Options) {
		o.Filename = logmanager.InstanceFilename(o.Filename, instanceID)
		options = append(options, o)
	}
	if c.Logging != nil && c.Logging.LogFile != "" {
		appendOption(c.Logging.ManagerOptions())
	}
	for _, backend := range c.Backends {
		if backend == nil || backend.AccessLog == nil {
			continue
		}
		if backend.AccessLog.Filename != "" {
			appendOption(backend.AccessLog.AccessManagerOptions())
		}
		if backend.AccessLog.ErrorFilename != "" {
			appendOption(backend.AccessLog.ErrorManagerOptions())
		}
	}
	return options
}
