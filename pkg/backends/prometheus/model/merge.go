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

package model

import (
	"encoding/json"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

// MakeMergeFunc creates a MergeFunc for a specific type that implements Merge
// The returned function accepts a type conforming to Mergeable[T] and merges it into the accumulator
func MakeMergeFunc[T any, PT merge.Mergeable[T]](errorType string,
	newInstance func() PT) merge.MergeFunc {
	return func(accum *merge.Accumulator, data any, idx int) error {
		var instance PT
		// Try to type assert to PT first
		if pt, ok := data.(PT); ok {
			instance = pt
		} else if body, ok := data.([]byte); ok {
			// If data is []byte, unmarshal it first (for backward compatibility during transition)
			instance = newInstance()
			if err := json.Unmarshal(body, instance); err != nil {
				logger.Error(errorType+" unmarshaling error",
					logging.Pairs{"provider": providers.Prometheus, "detail": err.Error()})
				return err
			}
		} else {
			// Not the expected type and not []byte
			return nil
		}
		accum.Lock()
		defer accum.Unlock()
		existing := accum.GetGenericUnsafe()
		if existing == nil {
			accum.SetGenericUnsafe(instance)
		} else {
			// Type assert to call Merge method
			if existingPT, ok := existing.(PT); ok {
				existingPT.Merge(instance)
			} else {
				// If type assertion fails, replace (shouldn't happen in practice)
				accum.SetGenericUnsafe(instance)
			}
		}
		return nil
	}
}

// MakeMergeFuncFromBytes creates a MergeFunc that accepts []byte and unmarshals it
// This is a convenience function for call sites that still have []byte
func MakeMergeFuncFromBytes[T any, PT merge.Mergeable[T]](errorType string,
	newInstance func() PT) func(*merge.Accumulator, []byte, int) error {
	mergeFunc := MakeMergeFunc(errorType, newInstance)
	return func(accum *merge.Accumulator, body []byte, idx int) error {
		instance := newInstance()
		if err := json.Unmarshal(body, instance); err != nil {
			logger.Error(errorType+" unmarshaling error",
				logging.Pairs{"provider": providers.Prometheus, "detail": err.Error()})
			return err
		}
		return mergeFunc(accum, instance, idx)
	}
}

// MakeRespondFunc creates a RespondFunc for a specific type that implements MarshallerPtr
func MakeRespondFunc[T any, PT merge.MarshallerPtr[T]](
	handleResult func(http.ResponseWriter, *http.Request, PT, int),
) merge.RespondFunc {
	return func(w http.ResponseWriter, r *http.Request,
		accum *merge.Accumulator, statusCode int) {
		generic := accum.GetGeneric()
		if generic == nil {
			return
		}
		if result, ok := generic.(PT); ok {
			handleResult(w, r, result, statusCode)
		}
	}
}
