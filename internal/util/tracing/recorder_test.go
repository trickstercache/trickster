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

package tracing

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/plugin/httptrace"
	"google.golang.org/grpc/codes"
)

func TestRecorder(t *testing.T) {
	flush, ctx, recorder, tr := setupTestingTracer(t, RecorderExporter, 1.0, testContextValues)

	err := tr.WithSpan(ctx, "Testing trace with span",
		func(ctx context.Context) error {
			var (
				err error
			)
			req, _ := http.NewRequest("GET", "https://example.com/test", nil)

			ctx, req = httptrace.W3C(ctx, req)
			httptrace.Inject(ctx, req)
			_, err = testHTTPClient().Do(req)
			if err != nil {
				return err
			}

			SpanFromContext(ctx).SetStatus(codes.OK)

			return nil

		})

	assert.NoError(t, err, "failed to inject span")
	flush()
	m := make(map[string]string)
	for _, kv := range testEvents {
		m[string(kv.Key)] = kv.Value.Emit()

	}

	for _, span := range recorder.spans {
		for _, msg := range span.MessageEvents {
			for _, attr := range msg.Attributes {
				key := string(attr.Key)
				wantV, ok := m[key]
				assert.True(t, ok, "kv not in known good map")
				v := attr.Value.Emit()
				assert.Equal(t, wantV, v, "Span Message attribute value incorrect")

			}
		}

	}

}
