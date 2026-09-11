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
	"bytes"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/stretchr/testify/require"
)

func TestSanitizeURL(t *testing.T) {
	require.Equal(t, "http://user:*****@10.0.0.1:9090/path",
		SanitizeURL("http://user:secret@10.0.0.1:9090/path"),
		"embedded credentials are masked")
	require.Equal(t, "http://10.0.0.1:9090",
		SanitizeURL("http://10.0.0.1:9090"),
		"credential-free URLs pass through")
	require.Equal(t, "http://user@10.0.0.1:9090",
		SanitizeURL("http://user@10.0.0.1:9090"),
		"username-only URLs pass through")
	require.Equal(t, "://not a url", SanitizeURL("://not a url"),
		"unparsable input is returned as-is")
}

func TestScopedLogging(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := logger.Logger()
	lg := logging.StreamLogger(buf, level.Debug)
	lg.SetLogAsynchronous(false)
	logger.SetLogger(lg)
	defer logger.SetLogger(prev)

	LogDebug("dbg-event", logging.Pairs{"k": "v"})
	LogInfo("info-event", nil)
	LogWarn("warn-event", logging.Pairs{"k": "v"})
	LogError("error-event", logging.Pairs{"k": "v"})

	out := buf.String()
	for _, event := range []string{"dbg-event", "info-event", "warn-event",
		"error-event"} {
		require.Contains(t, out, event)
	}
	require.Equal(t, 4, strings.Count(out, "scope="+LogScope),
		"every discovery log line carries the scope pair")
}
