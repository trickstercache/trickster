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

package profile

import "context"

type ContextType int

const ContextKey ContextType = 0

func FromContext(ctx context.Context) *Profile {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(ContextKey); v != nil {
		if ep, ok := v.(*Profile); ok {
			return ep
		}
	}
	return nil
}

func ToContext(ctx context.Context, ep *Profile) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, ContextKey, ep)
}
