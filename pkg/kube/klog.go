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

package kube

import (
	"fmt"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

var klogOnce sync.Once

// routeKlogOnce redirects client-go's klog output into Trickster's logger
// so operators get one log stream. Kubernetes informational chatter maps to
// debug (client-go is verbose at info); errors stay errors.
func routeKlogOnce() {
	klogOnce.Do(func() {
		klog.SetLogger(logr.New(&klogSink{}))
	})
}

// klogSink implements logr.LogSink over Trickster's logger
type klogSink struct {
	name string
	kv   []any
}

func (s *klogSink) Init(logr.RuntimeInfo) {}

func (s *klogSink) Enabled(int) bool { return true }

func (s *klogSink) Info(_ int, msg string, kv ...any) {
	logger.Debug(msg, s.pairs(kv))
}

func (s *klogSink) Error(err error, msg string, kv ...any) {
	p := s.pairs(kv)
	if err != nil {
		p["error"] = err.Error()
	}
	logger.Error(msg, p)
}

func (s *klogSink) WithValues(kv ...any) logr.LogSink {
	return &klogSink{name: s.name, kv: append(append([]any{}, s.kv...), kv...)}
}

func (s *klogSink) WithName(name string) logr.LogSink {
	if s.name != "" {
		name = s.name + "." + name
	}
	return &klogSink{name: name, kv: s.kv}
}

func (s *klogSink) pairs(kv []any) logging.Pairs {
	p := logging.Pairs{"scope": "kubernetes"}
	if s.name != "" {
		p["logger"] = s.name
	}
	all := append(append([]any{}, s.kv...), kv...)
	for i := 0; i+1 < len(all); i += 2 {
		key, ok := all[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", all[i])
		}
		p[key] = all[i+1]
	}
	return p
}
