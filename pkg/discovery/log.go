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

package discovery

import (
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// LogScope is the scope pair value attached to every autodiscovery log
// event (providers and the ALB dynamic pool managers), so operators can
// filter the discovery control plane into one stream.
const LogScope = "discovery"

func scoped(detail logging.Pairs) logging.Pairs {
	if detail == nil {
		detail = logging.Pairs{}
	}
	detail["scope"] = LogScope
	return detail
}

// LogDebug logs a discovery-scoped debug event
func LogDebug(event string, detail logging.Pairs) {
	logger.Debug(event, scoped(detail))
}

// LogInfo logs a discovery-scoped info event
func LogInfo(event string, detail logging.Pairs) {
	logger.Info(event, scoped(detail))
}

// LogWarn logs a discovery-scoped warning event
func LogWarn(event string, detail logging.Pairs) {
	logger.Warn(event, scoped(detail))
}

// LogError logs a discovery-scoped error event
func LogError(event string, detail logging.Pairs) {
	logger.Error(event, scoped(detail))
}

// SanitizeURL masks any credential embedded in the provided URL so
// discovered origins are always safe to log; unparsable input is returned
// as-is (our member URLs are constructed, not user-typed)
func SanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return raw
	}
	username := u.User.Username()
	u.User = nil
	rest := u.String()
	i := strings.Index(rest, "://")
	if i < 0 {
		// no scheme separator to re-anchor the userinfo; the
		// credential-free form is the safe rendering
		return rest
	}
	// rebuilt by hand so the mask renders literally rather than
	// percent-encoded
	return rest[:i+3] + username + ":*****@" + rest[i+3:]
}
