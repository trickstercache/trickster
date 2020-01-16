/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package urls

import "net/url"

// CloneURL returns a deep copy of a *url.URL
func CloneURL(u *url.URL) *url.URL {
	u2 := &url.URL{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Path:     u.Path,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}
	if u.User != nil {
		var user *url.Userinfo
		if p, ok := u.User.Password(); ok {
			user = url.UserPassword(u.User.Username(), p)
		} else {
			user = url.User(u.User.Username())
		}
		u2.User = user
	}

	return u2
}
