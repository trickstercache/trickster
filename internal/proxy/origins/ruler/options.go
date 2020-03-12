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

package ruler

type rewriteList [][]string

// RuleOptions defines the options for a Rule
type RuleOptions struct {
	// NextRoute indicates the name of the next OriginConfig destination for the request when
	// none of the cases are met following the execution of the rule
	NextRoute string `toml:"next_route"`
	// Input source specifies the data source used when executing the rule. Possible options:
	//  Source           Example Source Used
	//  url              https://example.com:8480/path1/path2?param1=value
	//  url_no_params    https://example.com:8480/path1/path2
	//  scheme           https
	//  host             example.com:8480
	//  hostname         example.com
	//  port             8480 (80 and 443 are auto-set based on scheme when no port is provided)
	//  path             /path1/path2
	//  params           ?param1=value
	//  param            [must be used with InputKey as described below]
	//  header           [must be used with InputKey as described below]
	InputSource string `toml:"input_source"`
	// PreOpRewrite is a list of URL and Header rewrite instructions that permanently modifiy the
	// http request prior to executing the rule
	PreOpRewrite rewriteList `toml:"rewrite"`
	// PostOpRewrite is a list of URL and Header rewrite instructions that permanently modifiy the
	// http request after executing the rule
	PostOpRewrite rewriteList `toml:"rewrite"`
	// InputKey is optional and provides extra information for locating the data source
	// when the InputSource is header or param, the input key must be the target header or param name
	InputKey string `toml:"input_key"`
	// InputType is optional, defaulting to string, and indicates the type of input:
	// string, num (treated internally as float64), or bool
	InputType string `toml:"input_type"`
	// InputEncoding is optional, defaulting to '', and defines any special encoding format on
	// the input. Supported Options are: 'base64'
	InputEncoding string `toml:"input_encoding"`
	// InputIndex is optional, defaulting to -1 (no parts / use full string), and indicates which part
	// of the Input contains the specific value to which this rule applies. InputIndex is zero-based.
	InputIndex int `toml:"input_index"`
	// InputDelimiter is optional, defaulting to " ", and indicates the delimiter for separating the Input
	// into parts. This value has no effect unless InputIndex >= 0
	InputDelimiter int `toml:"input_delimiter"`
	// Operation specifies what action to take on the input, whose result is used to
	// determine if any case is matched. Possible options are as follows.
	// string:   eq, contains, suffix, prefix, md5, sha1, base64, modulo
	// num:      eq, gt, lt, ge, le, bt (inclusive), modulo
	// bool:     eq
	// any boolean operation (everything but md5, sha1, base64, modulo) can be prefixed with !
	Operation string `toml:"operation"`
	// OperationArg is optional and provides extra information used when performing the
	// configured Operation, such as the demonimator when the operation is modulus
	OperationArg string `toml:"operation_arg"`
	// RuleCaseOptions is the map of cases to apply to evaluate against this rule
	CaseOptions map[string]*CaseOptions `toml:"cases"`
}

// CaseOptions defines the options for a given evaluation case
type CaseOptions struct {
	// Matches indicates the values matching the rule execution's output that apply to this case
	Matches []string `toml:"matches"`
	// Rewrite is a list of URL and Header rewrite instructions that modify the request in this case
	// prior to handing off to the NextRoute
	Rewrite rewriteList `toml:"rewrite"`
	// NextRoute is the name of the next OriginConfig destination for the request in this case
	NextRoute string `toml:"next_route"`
}

/*

Example TOML Config:

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
					['header', 'replace', 'Cache-Control', '${match}, 'myReplacement'],
					['header', 'delete', 'Cache-Control', '${match}] ]

		next_route = 'origin2'
*/
