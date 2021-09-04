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

package rule

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
)

var ErrInvalidRegularExpression = errors.New("invalid regular expression")

func (c *Client) parseOptions(o *ro.Options, rwi map[string]rewriter.RewriteInstructions) error {

	name := c.Name()

	if o == nil {
		return fmt.Errorf("rule client %s failed to parse nil options", name)
	}

	if o.InputSource == "" {
		return fmt.Errorf("rule client %s options missing input_source", name)
	}

	if o.InputType == "" {
		return fmt.Errorf("rule client %s options missing input_type", name)
	}

	if o.Operation == "" {
		return fmt.Errorf("rule client %s options missing operation", name)
	}

	if o.MaxRuleExecutions == 0 {
		o.MaxRuleExecutions = ro.DefaultMaxRuleExecutions
	}

	var nr http.Handler
	r := &rule{maxRuleExecutions: o.MaxRuleExecutions}

	if o.EgressReqRewriterName != "" {
		ri, ok := rwi[o.EgressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid egress rewriter %s in rule %s",
				o.EgressReqRewriterName, o.Name)
		}
		r.egressReqRewriter = ri
	}

	if o.IngressReqRewriterName != "" {
		ri, ok := rwi[o.IngressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid ingress rewriter %s in rule %s",
				o.IngressReqRewriterName, o.Name)
		}
		r.ingressReqRewriter = ri
	}

	if o.NoMatchReqRewriterName != "" {
		ri, ok := rwi[o.NoMatchReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid default rewriter %s in rule %s",
				o.NoMatchReqRewriterName, o.Name)
		}
		r.defaultRewriter = ri
	}

	badDefaultRoute := fmt.Errorf("invalid default rule route %s in rule %s",
		o.NextRoute, o.Name)

	if o.NextRoute != "" {
		nc := c.clients.Get(o.NextRoute)
		if nc == nil || nc.Router() == nil {
			return badDefaultRoute
		}
		nr = nc.Router()
	} else if o.RedirectURL != "" {
		r.defaultRedirectURL = o.RedirectURL
		r.defaultRedirectCode = 302
		nr = http.HandlerFunc(handlers.HandleRedirectResponse)
	} else {
		return badDefaultRoute
	}

	r.defaultRouter = nr

	exf, ok := isValidSourceName(o.InputSource)
	if !ok {
		return fmt.Errorf("invalid source name %s in rule %s", o.InputSource, o.Name)
	}
	r.extractionFunc = exf
	r.extractionArg = o.InputKey

	// if the user only wants a part of the response
	if o.InputIndex > -1 && o.InputDelimiter != "" {
		f := r.extractionFunc
		r.extractionFunc = func(hr *http.Request, arg string) string {
			return extractSourcePart(f(hr, arg), o.InputDelimiter, o.InputIndex)
		}
	}

	// if the user needs to decode the input
	if o.InputEncoding != "" {
		f := r.extractionFunc
		df, ok := decodingFuncs[encoding(o.InputEncoding)]
		if !ok {
			return fmt.Errorf("invalid encoding name %s in rule %s", o.InputEncoding, o.Name)
		}
		r.extractionFunc = func(hr *http.Request, arg string) string {
			return df(f(hr, arg), "", 0)
		}
	}

	if strings.HasPrefix(o.Operation, "!") {
		r.negateOpResult = true
		o.Operation = o.Operation[1:]
	}

	of, ok := operationFuncs[operation(o.InputType+"-"+o.Operation)]
	if !ok {
		return fmt.Errorf("invalid operation %s in rule %s", o.InputType+"-"+o.Operation, o.Name)
	}
	r.operationFunc = of
	r.operationArg = o.OperationArg
	if r.operationArg == "" {
		r.evaluatorFunc = r.EvaluateCaseArg
	} else {
		r.evaluatorFunc = r.EvaluateOpArg
	}

	if o.InputType == "rmatch" {
		if r.operationArg == "" {
			return ErrInvalidRegularExpression
		}
		re, err := regexp.Compile(r.operationArg)
		if err != nil {
			return err
		}
		compiledRegexes[r.operationArg] = re
	}

	if len(o.CaseOptions) > 0 {

		r.cases = make(caseMap)
		r.caseList = make(caseList, 0)

		for k, v := range o.CaseOptions {

			var ri rewriter.RewriteInstructions
			if v.ReqRewriterName != "" {
				i, ok := rwi[v.ReqRewriterName]
				if !ok {
					return fmt.Errorf("invalid rewriter %s in rule %s case %s", k, o.Name, k)
				}
				ri = i
			}

			if v.NextRoute == "" && v.RedirectURL == "" && v.ReqRewriterName == "" {
				return fmt.Errorf("missing next_route in rule %s case %s", o.Name, k)
			}

			if len(v.Matches) == 0 {
				return fmt.Errorf("missing matches in rule %s case %s", o.Name, k)
			}

			rc := 0
			if v.RedirectURL != "" {
				rc = 302
				nr = http.HandlerFunc(handlers.HandleRedirectResponse)
			} else if v.NextRoute != "" {
				no, ok := c.clients[v.NextRoute]
				if !ok {
					return fmt.Errorf("unknown next_route %s in rule %s case %s",
						v.NextRoute, o.Name, k)
				}
				nr = no.Router()
			}

			for _, m := range v.Matches {
				rc := &ruleCase{
					matchValue:   m,
					router:       nr,
					redirectURL:  v.RedirectURL,
					redirectCode: rc,
					rewriter:     ri,
				}
				r.caseList = append(r.caseList, rc)
				r.cases[m] = rc
			}
		}
	}

	c.rule = r
	return nil
}
