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

	"github.com/Comcast/trickster/internal/proxy/handlers"
	ro "github.com/Comcast/trickster/internal/proxy/origins/rule/options"
	"github.com/Comcast/trickster/internal/proxy/request/rewriter"
)

func (c *Client) parseOptions(ro *ro.Options, rwi map[string]rewriter.RewriteInstructions) error {

	if ro == nil {
		return fmt.Errorf("rule client %s failed to parse nil options", c.name)
	}

	if ro.InputSource == "" {
		return fmt.Errorf("rule client %s options missing input_type", c.name)
	}

	if ro.InputType == "" {
		return fmt.Errorf("rule client %s options missing input_type", c.name)
	}

	if ro.Operation == "" {
		return fmt.Errorf("rule client %s options missing operation", c.name)
	}

	var nr http.Handler
	r := &rule{}

	if ro.EgressReqRewriterName != "" {
		ri, ok := rwi[ro.EgressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid  egress rewriter %s in rule %s",
				ro.EgressReqRewriterName, ro.Name)
		}
		r.egressReqRewriter = ri
	}

	if ro.IngressReqRewriterName != "" {
		ri, ok := rwi[ro.IngressReqRewriterName]
		if !ok {
			return fmt.Errorf("invalid  ingress rewriter %s in rule %s",
				ro.IngressReqRewriterName, ro.Name)
		}
		r.ingressReqRewriter = ri
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

	if ro.InputEncoding != "" {
		df, ok := decodingFuncs[encoding(ro.InputEncoding)]
		if !ok {
			return fmt.Errorf("invalid encoding name %s in rule %s", ro.InputEncoding, ro.Name)
		}
		r.decodingFunc = df
	}

	if strings.HasPrefix(ro.Operation, "!") {
		r.negateOpResult = true
		ro.Operation = ro.Operation[1:]
	}

	of, ok := operationFuncs[operation(ro.InputType+"-"+ro.Operation)]
	if !ok {
		return fmt.Errorf("invalid encoding name %s in rule %s", ro.InputEncoding, ro.Name)
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

			if v.NextRoute == "" && v.RedirectURL == "" {
				return fmt.Errorf("missing next_route in rule %s case %s", ro.Name, k)
			}

			if len(v.Matches) == 0 {
				return fmt.Errorf("missing matches in rule %s case %s", ro.Name, k)
			}

			rc := 0
			if v.RedirectURL != "" {
				rc = 302
				nr = http.HandlerFunc(handlers.HandleRedirectResponse)
			} else {
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

/*

 This example TOML serves as a TODO for parts of the rules engine left to fully implement

[rules]
  [rules.example]
  # input_index = -1              # set to a value >= 0 to use a part of the input for the operation
  # input_delimiter = ' '         # when input_index >=0, this is used to split the input into parts

  [rules.example.cases]
		[rules.example.cases.1]
		rewrite = [ ['path', 'replace', '${match}', 'myReplacement'],
					['header', 'set', 'Cache-Control', 'myReplacement'],
					['header', 'replace', 'Cache-Control', '${match}', 'myReplacement'],
					['header', 'delete', 'Cache-Control', '${match}'] ]
*/
