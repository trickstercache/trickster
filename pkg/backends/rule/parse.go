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
	"fmt"
	"net/http"
	"strings"

	ro "github.com/tricksterproxy/trickster/pkg/backends/rule/options"
	"github.com/tricksterproxy/trickster/cmd/trickster/config/defaults"
	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter"
)

func (c *Client) parseOptions(ro *ro.Options, rwi map[string]rewriter.RewriteInstructions) error {

	name := c.Name()

	if ro == nil {
		return fmt.Errorf("rule client %s failed to parse nil options", name)
	}

	if ro.InputSource == "" {
		return fmt.Errorf("rule client %s options missing input_source", name)
	}

	if ro.InputType == "" {
		return fmt.Errorf("rule client %s options missing input_type", name)
	}

	if ro.Operation == "" {
		return fmt.Errorf("rule client %s options missing operation", name)
	}

	if ro.MaxRuleExecutions == 0 {
		ro.MaxRuleExecutions = defaults.DefaultMaxRuleExecutions
	}

	var nr http.Handler
	r := &rule{maxRuleExecutions: ro.MaxRuleExecutions}

	if ro.EgressReqRewriterName != "" {
		ri, ok := rwi[ro.EgressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid egress rewriter %s in rule %s",
				ro.EgressReqRewriterName, ro.Name)
		}
		r.egressReqRewriter = ri
	}

	if ro.IngressReqRewriterName != "" {
		ri, ok := rwi[ro.IngressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid ingress rewriter %s in rule %s",
				ro.IngressReqRewriterName, ro.Name)
		}
		r.ingressReqRewriter = ri
	}

	if ro.NoMatchReqRewriterName != "" {
		ri, ok := rwi[ro.NoMatchReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid default rewriter %s in rule %s",
				ro.NoMatchReqRewriterName, ro.Name)
		}
		r.defaultRewriter = ri
	}

	badDefaultRoute := fmt.Errorf("invalid default rule route %s in rule %s",
		ro.NextRoute, ro.Name)

	if ro.NextRoute != "" {
		nc := c.clients.Get(ro.NextRoute)
		if nc == nil || nc.Router() == nil {
			return badDefaultRoute
		}
		nr = nc.Router()
	} else if ro.RedirectURL != "" {
		r.defaultRedirectURL = ro.RedirectURL
		r.defaultRedirectCode = 302
		nr = http.HandlerFunc(handlers.HandleRedirectResponse)
	} else {
		return badDefaultRoute
	}

	r.defaultRouter = nr

	exf, ok := isValidSourceName(ro.InputSource)
	if !ok {
		return fmt.Errorf("invalid source name %s in rule %s", ro.InputSource, ro.Name)
	}
	r.extractionFunc = exf
	r.extractionArg = ro.InputKey

	// if the user only wants a part of the response
	if ro.InputIndex > -1 && ro.InputDelimiter != "" {
		f := r.extractionFunc
		r.extractionFunc = func(hr *http.Request, arg string) string {
			return extractSourcePart(f(hr, arg), ro.InputDelimiter, ro.InputIndex)
		}
	}

	// if the user needs to decode the input
	if ro.InputEncoding != "" {
		f := r.extractionFunc
		df, ok := decodingFuncs[encoding(ro.InputEncoding)]
		if !ok {
			return fmt.Errorf("invalid encoding name %s in rule %s", ro.InputEncoding, ro.Name)
		}
		r.extractionFunc = func(hr *http.Request, arg string) string {
			return df(f(hr, arg), "", 0)
		}
	}

	if strings.HasPrefix(ro.Operation, "!") {
		r.negateOpResult = true
		ro.Operation = ro.Operation[1:]
	}

	of, ok := operationFuncs[operation(ro.InputType+"-"+ro.Operation)]
	if !ok {
		return fmt.Errorf("invalid operation %s in rule %s", ro.InputType+"-"+ro.Operation, ro.Name)
	}
	r.operationFunc = of
	r.operationArg = ro.OperationArg
	if r.operationArg == "" {
		r.evaluatorFunc = r.EvaluateCaseArg
	} else {
		r.evaluatorFunc = r.EvaluateOpArg
	}

	if len(ro.CaseOptions) > 0 {

		r.cases = make(caseMap)
		r.caseList = make(caseList, 0)

		for k, v := range ro.CaseOptions {

			var ri rewriter.RewriteInstructions
			if v.ReqRewriterName != "" {
				i, ok := rwi[v.ReqRewriterName]
				if !ok {
					return fmt.Errorf("invalid rewriter %s in rule %s case %s", k, ro.Name, k)
				}
				ri = i
			}

			if v.NextRoute == "" && v.RedirectURL == "" && v.ReqRewriterName == "" {
				return fmt.Errorf("missing next_route in rule %s case %s", ro.Name, k)
			}

			if len(v.Matches) == 0 {
				return fmt.Errorf("missing matches in rule %s case %s", ro.Name, k)
			}

			rc := 0
			if v.RedirectURL != "" {
				rc = 302
				nr = http.HandlerFunc(handlers.HandleRedirectResponse)
			} else if v.NextRoute != "" {
				no, ok := c.clients[v.NextRoute]
				if !ok {
					return fmt.Errorf("unknown next_route %s in rule %s case %s",
						v.NextRoute, ro.Name, k)
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
