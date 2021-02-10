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

package prometheus

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/proxy/engines"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
)

const pathFix string = `
`

// GraphHandler routes to the graph endpoint of the Configured Upstream Origin
func (c *Client) GraphHandler(w http.ResponseWriter, r *http.Request) {

	// ajaxBaseURL := "http"
	// if r.TLS != nil {
	// 	ajaxBaseURL += "s"
	// }
	// // TODO: Account for host-based requests where c.name would generate a 404 on the upstream
	// ajaxBaseURL += "://" + r.Host + "/" + c.name

	r.URL = urls.BuildUpstreamURL(r, c.BaseUpstreamURL())

	// writer := bytes.NewBuffer(nil)
	engines.DoProxy(w, r, true)
	// 	body := strings.Replace(strings.Replace(string(writer.Bytes()), `href="/`, `href="/`+c.name+`/`, -1), `src="/`, `src="/`+c.name+`/`, -1)

	// 	// this instructs jQuery in the Prom Graph to use /backendName in the ajax base URL path
	// 	bodyMod := "            var BASE_URL = '" + ajaxBaseURL + "';" + `
	//             $.ajaxSetup({
	//                 beforeSend: function(xhr, options) {
	//                     options.url = BASE_URL + options.url;
	//                 }
	//             });
	// `
	// 	body = strings.Replace(body, "<script>\n", "<script>\n"+bodyMod, 1)

	// 	wh := w.Header()
	// 	headers.Merge(wh, resp.Header)
	// 	w.Write([]byte(body))
}
