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
	"errors"
	"fmt"

	ro "github.com/Comcast/trickster/internal/proxy/origins/rule/options"
	"github.com/gorilla/mux"
)

func (c *Client) parseOptions(ro *ro.Options) error {

	if ro == nil {
		return errors.New("rule client unable to parse nil options")
	}

	nc := c.clients.Get(ro.NextRoute)
	if nc == nil || nc.Router() == nil {
		return fmt.Errorf("invalid default rule route %s", ro.NextRoute)
	}

	c.rule = &rule{defaultRouter: nc.Router().(*mux.Router)}

	return nil
}

//func

/*
[rules]
  [rules.example]
  input_source = 'header'       # path, host, param
  input_key = 'Cache-Control'
  input_type = 'string'         # num, bool, date, string
  # input_encoding = ''           # set to 'base64' to decode Authorization header, etc.
  # input_index = -1              # set to a value >= 0 to use a part of the input for the operation
  # input_delimiter = ' '         # when input_index >=0, this is used to split the input into parts
  operation = 'contains'        # prefix, suffix, contains, eq, le, ge, gt, lt, modulo, md5, sha1
  # operation_arg = '7'           # use to set a modulo operation's denominator, or path depth
  next_route = 'origin1'
	[rules.example.cases]
		[rules.example.cases.1]
		matches = ['no-cache', 'no-store']
		rewrite = [ ['path', 'replace', '${match}', 'myReplacement'],
					['header', 'set', 'Cache-Control', 'myReplacement'],
					['header', 'replace', 'Cache-Control', '${match}', 'myReplacement'],
					['header', 'delete', 'Cache-Control', '${match}'] ]
		next_route = 'origin2'
*/
