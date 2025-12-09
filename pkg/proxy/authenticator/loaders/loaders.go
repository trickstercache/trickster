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

package loaders

import (
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/loaders/csv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/loaders/htpasswd"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func LoadData(path string, ff types.CredentialsFileFormat) (types.CredentialsManifest, error) {
	switch ff {
	case types.HTPasswd:
		return htpasswd.LoadHtpasswdBcrypt(path)
	case types.CSV, types.CSVNoHeader:
		return csv.LoadCSV(path, ff)
	}
	return nil, nil
}
