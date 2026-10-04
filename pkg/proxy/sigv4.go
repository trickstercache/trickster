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

package proxy

import (
	"sync/atomic"
	"time"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

const (
	// sigV4WarnInterval throttles each SigV4 warning to once per interval per client
	sigV4WarnInterval = time.Minute

	sigV4EventSignFailure     = "sign_failure"
	sigV4EventCredentialRetry = "credentials_retry"
)

// NewSigner returns the SigV4 signer for o's sigv4 block, or nil when it has none.
func NewSigner(o *bo.Options) (*taws.Signer, error) {
	if o == nil || o.SigV4 == nil {
		return nil, nil
	}
	return taws.NewSigner(o.SigV4)
}

// sigV4Observer counts a backend's SigV4 events and logs each kind at most once per sigV4WarnInterval.
func sigV4Observer(backendName string) taws.Observer {
	signFailed := throttledWarn()
	retried := throttledWarn()
	return taws.Observer{
		SignFailed: func(err error) {
			metrics.ProxySigV4Events.WithLabelValues(backendName, sigV4EventSignFailure).Inc()
			signFailed("sigv4 signing failed; the request was not sent",
				logging.Pairs{keys.BackendName: backendName, keys.Error: err})
		},
		CredentialsRetried: func(code string) {
			metrics.ProxySigV4Events.WithLabelValues(backendName, sigV4EventCredentialRetry).Inc()
			retried("sigv4 credentials rejected by the origin; refreshed them and resent the request",
				logging.Pairs{keys.BackendName: backendName, keys.Code: code})
		},
	}
}

// throttledWarn returns a func that logs a warning unless it already logged one within sigV4WarnInterval.
func throttledWarn() func(string, logging.Pairs) {
	return throttledWarnWith(logger.Warn)
}

func throttledWarnWith(warn func(string, logging.Pairs)) func(string, logging.Pairs) {
	var last atomic.Int64
	return func(event string, detail logging.Pairs) {
		nowNano, prevNano := time.Now().UnixNano(), last.Load()
		if (prevNano != 0 && nowNano-prevNano < int64(sigV4WarnInterval)) ||
			!last.CompareAndSwap(prevNano, nowNano) {
			return
		}
		warn(event, detail)
	}
}
