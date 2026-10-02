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

package aws

import (
	"context"
	"regexp"
	"strings"
)

// Scope is the service and region a SigV4 signature is made for.
type Scope struct {
	Service string
	Region  string
}

const credentialField = "Credential=" // #nosec G101 -- a field name of the Authorization header

// regionPattern matches AWS region names such as us-east-1 or us-gov-west-1, and nothing that
// could alter a hostname built from one
var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)

type (
	clientScopeKey  struct{}
	signingScopeKey struct{}
)

// ValidRegion reports whether region is shaped like an AWS region name.
func ValidRegion(region string) bool {
	return regionPattern.MatchString(region)
}

// ParseScope returns the scope of a SigV4 Authorization value, whose credential reads
// AKID/date/region/service/aws4_request.
func ParseScope(authorization string) (Scope, bool) {
	_, cred, ok := strings.Cut(authorization, credentialField)
	if !ok {
		return Scope{}, false
	}
	cred, _, _ = strings.Cut(cred, ",")
	parts := strings.Split(strings.TrimSpace(cred), "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || parts[2] == "" || parts[3] == "" {
		return Scope{}, false
	}
	return Scope{Service: parts[3], Region: parts[2]}, true
}

// WithClientScope returns ctx recording the scope a client's own signature was made for.
func WithClientScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, clientScopeKey{}, s)
}

// ClientScope returns the scope recorded by WithClientScope.
func ClientScope(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(clientScopeKey{}).(Scope)
	return s, ok
}

// WithSigningScope returns ctx whose requests a Signer signs for s rather than its own service
// and region; an empty field keeps the Signer's own.
func WithSigningScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, signingScopeKey{}, s)
}

// signingScope returns the service and region to sign a request made with ctx for.
func signingScope(ctx context.Context, service, region string) (string, string) {
	if s, ok := ctx.Value(signingScopeKey{}).(Scope); ok {
		if s.Service != "" {
			service = s.Service
		}
		if s.Region != "" {
			region = s.Region
		}
	}
	return service, region
}
