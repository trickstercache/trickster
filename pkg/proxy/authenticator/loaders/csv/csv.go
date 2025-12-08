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

package csv

import (
	"encoding/csv"
	"os"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
)

func LoadCSV(path string, ff types.CredentialsFileFormat,
	cf types.CredentialsFormat,
) (types.CredentialsManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	matrix, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var start int
	if ff == types.CSV {
		start = 1
	}
	out := make(types.CredentialsManifest, len(matrix)-start)
	for i, row := range matrix {
		if i < start || len(row) < 2 {
			continue
		}
		p, err := cred.ProcessRawCredential(strings.TrimSpace(row[1]), cf)
		if err != nil {
			return nil, err
		}
		out[strings.TrimSpace(row[0])] = p
	}
	return out, nil
}
