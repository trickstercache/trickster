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

package types

type AuthResult struct {
	Status          AuthResultStatus
	StatusDetail    string
	Username        string
	ResponseHeaders map[string]string
	// Claims, etc. would go here in the future as needed
}

type AuthResultStatus int

const (
	AuthUnknown AuthResultStatus = iota
	AuthSuccess
	AuthFailed
	AuthMissing
	// AuthObserved is for when the username was read by the Authenticator but
	// credentials were not verified because the Authenticator is observe-only
	AuthObserved
)
