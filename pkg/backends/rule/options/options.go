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

// Options defines the options for a Rule
type Options struct {
	// Name provides the name of the Rule
	Name string `yaml:"-"`
	// NextRoute indicates the name of the next BackendOptions destination for the request when
	// none of the cases are met following the execution of the rule
	NextRoute string `yaml:"next_route,omitempty"`
	// IngressReqRewriterName is the name of a configured Rewriter that will modify the request prior
	// to the rule taking any other action
	IngressReqRewriterName string `yaml:"ingress_req_rewriter_name,omitempty"`
	// EgressReqRewriterName is the name of a configured Rewriter that will modify the request once
	// all other rule actions have occurred, prior to the request being passed to the next route
	EgressReqRewriterName string `yaml:"egress_req_rewriter_name,omitempty"`
	// NoMatchReqRewriterName is the name of a configured Rewriter that will modify the request once
	// all other rule actions have occurred, and only if the Request did not match any defined case,
	// prior to the request being passed to the next route
	NoMatchReqRewriterName string `yaml:"nomatch_req_rewriter_name,omitempty"`
	//
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
	InputSource string `yaml:"input_source,omitempty"`
	//
	// InputKey is optional and provides extra information for locating the data source
	// when the InputSource is header or param, the input key must be the target header or param name
	InputKey string `yaml:"input_key,omitempty"`
	// InputType is optional, defaulting to string, and indicates the type of input:
	// string, num (treated internally as float64), or bool
	InputType string `yaml:"input_type,omitempty"`
	// InputEncoding is optional, defaulting to '', and defines any special encoding format on
	// the input. Supported Options are: 'base64'
	InputEncoding string `yaml:"input_encoding,omitempty"`
	// InputIndex is optional, defaulting to -1 (no parts / use full string), and indicates which part
	// of the Input contains the specific value to which this rule applies. InputIndex is zero-based.
	InputIndex int `yaml:"input_index,omitempty"`
	// InputDelimiter is optional, defaulting to " ", and indicates the delimiter for separating the Input
	// into parts. This value has no effect unless InputIndex >= 0
	InputDelimiter string `yaml:"input_delimiter,omitempty"`
	//
	// Operation specifies what action to take on the input, whose result is used to
	// determine if any case is matched. Possible options are as follows.
	// string:   eq, contains, suffix, prefix, md5, sha1, base64, modulo
	// num:      eq, gt, lt, ge, le, bt (inclusive), modulo
	// bool:     eq
	// any boolean operation (everything but md5, sha1, base64, modulo) can be prefixed with !
	Operation string `yaml:"operation,omitempty"`
	//
	// OperationArg is optional and provides extra information used when performing the
	// configured Operation, such as the demonimator when the operation is modulus
	OperationArg string `yaml:"operation_arg,omitempty"`
	// RuleCaseOptions is the map of cases to apply to evaluate against this rule
	CaseOptions map[string]*CaseOptions `yaml:"cases,omitempty"`
	// RedirectURL provides a URL to redirect the request in the default case, rather than
	// handing off to the NextRoute
	RedirectURL string `yaml:"redirect_url,omitempty"`
	// MaxRuleExecutions limits the maximum number of per-Request rule-based hops so as to avoid
	// execution loops.
	MaxRuleExecutions int `yaml:"max_rule_executions,omitempty"`
}

// CaseOptions defines the options for a given evaluation case
type CaseOptions struct {
	// Matches indicates the values matching the rule execution's output that apply to this case
	Matches []string `yaml:"matches,omitempty"`
	// ReqRewriterName is the name of a configured Rewriter that will modify the request in this case
	// prior to handing off to the NextRoute
	ReqRewriterName string `yaml:"req_rewriter_name,omitempty"`
	// NextRoute is the name of the next BackendOptions destination for the request in this case
	NextRoute string `yaml:"next_route,omitempty"`
	// RedirectURL provides a URL to redirect the request in this case, rather than
	// handing off to the NextRoute
	RedirectURL string `yaml:"redirect_url,omitempty"`
}

// Lookup is a map of Options
type Lookup map[string]*Options

// Clone returns a perfect copy of the subject *Options
func (o *Options) Clone() *Options {
	return &Options{
		Name:                   o.Name,
		NextRoute:              o.NextRoute,
		IngressReqRewriterName: o.IngressReqRewriterName,
		EgressReqRewriterName:  o.EgressReqRewriterName,
		NoMatchReqRewriterName: o.NoMatchReqRewriterName,
		InputSource:            o.InputSource,
		InputKey:               o.InputKey,
		InputType:              o.InputType,
		InputEncoding:          o.InputEncoding,
		InputIndex:             o.InputIndex,
		InputDelimiter:         o.InputDelimiter,
		Operation:              o.Operation,
		OperationArg:           o.OperationArg,
		CaseOptions:            o.CaseOptions,
		RedirectURL:            o.RedirectURL,
		MaxRuleExecutions:      o.MaxRuleExecutions,
	}
}
