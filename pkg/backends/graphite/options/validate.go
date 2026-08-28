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

package options

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

var (
	// ErrOriginAuthConflict is returned when origin_authorization is set
	// alongside origin_username/origin_password
	ErrOriginAuthConflict = errors.New(
		"origin_authorization is mutually exclusive with origin_username/origin_password")
	// ErrOriginAuthNoUser is returned when origin_password is set without
	// origin_username
	ErrOriginAuthNoUser = errors.New("origin_password requires origin_username")
	// ErrOriginAuthAppend is returned when a path appends (+) Authorization
	// while a backend-wide origin credential is configured
	ErrOriginAuthAppend = errors.New(
		"origin credential conflicts with a '+Authorization' request_headers operation")
)

// AuthHeader renders the configured origin credential as an Authorization
// header value; empty when none is configured
func (o *Options) AuthHeader() (string, error) {
	if o == nil {
		return "", nil
	}
	if o.OriginAuthorization != "" {
		if o.OriginUsername != "" || o.OriginPassword != "" {
			return "", ErrOriginAuthConflict
		}
		return o.OriginAuthorization, nil
	}
	if o.OriginUsername == "" {
		if o.OriginPassword != "" {
			return "", ErrOriginAuthNoUser
		}
		return "", nil
	}
	return "Basic " + base64.StdEncoding.EncodeToString(
		[]byte(o.OriginUsername+":"+o.OriginPassword)), nil
}

// ValidateWithPaths checks the declarative origin-auth option combinations,
// including conflicts with the given path configurations' request_headers
func (o *Options) ValidateWithPaths(paths po.List) error {
	auth, err := o.AuthHeader()
	if err != nil {
		return err
	}
	if auth == "" {
		return nil
	}
	for _, pc := range paths {
		if pc == nil {
			continue
		}
		for k := range pc.RequestHeaders {
			if strings.HasPrefix(k, "+") &&
				http.CanonicalHeaderKey(k[1:]) == headers.NameAuthorization {
				return fmt.Errorf("%w (path %s)", ErrOriginAuthAppend, pc.Path)
			}
		}
	}
	return nil
}
