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

package rule

import (
	"github.com/gorilla/mux"
)

type rule struct {
	defaultRouter  *mux.Router
	extractionFunc extractionFunc
	decodingFunc   decodingFunc
	operationFunc  operationFunc
}

type RuleClients []*Client

func (rc RuleClients) Load() error {
	for _, c := range rc {
		if c != nil {
			if err := c.parseOptions(c.options.RuleOptions); err != nil {
				return err
			}
		}
	}
	return nil
}
