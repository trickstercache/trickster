package merge

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// Timeseries merges the provided Responses into a single Timeseries Dataset
// and writes it to the provided responsewriter
func Timeseries(w http.ResponseWriter, r *http.Request, rgs ResponseGates) {

	var ts timeseries.Timeseries
	var f timeseries.MarshalWriterFunc
	var rlo *timeseries.RequestOptions

	responses := make([]int, len(rgs))
	var bestResp *http.Response

	h := w.Header()
	tsm := make([]timeseries.Timeseries, 0)
	for i, rg := range rgs {

		if rg == nil || rg.Resources == nil ||
			rg.Resources.Response == nil {
			continue
		}

		resp := rg.Resources.Response
		responses[i] = resp.StatusCode

		if rg.Resources.TS != nil {
			headers.Merge(h, rg.Header())
			if f == nil && rg.Resources.TSMarshaler != nil {
				f = rg.Resources.TSMarshaler
			}
			if rlo == nil {
				rlo = rg.Resources.TSReqestOptions
			}
			if ts == nil {
				ts = rg.Resources.TS
				continue
			}
			tsm = append(tsm, rg.Resources.TS)
		}
		if bestResp == nil || resp.StatusCode < bestResp.StatusCode {
			bestResp = resp
			resp.Body = ioutil.NopCloser(bytes.NewReader(rg.Body()))
		}
	}

	if ts == nil || f == nil {
		if bestResp != nil {
			h := w.Header()
			headers.Merge(h, bestResp.Header)
			w.WriteHeader(bestResp.StatusCode)
			io.Copy(w, bestResp.Body)
		} else {
			handlers.HandleBadGateway(w, r)
		}
		return
	}

	statusCode := 200
	if bestResp != nil {
		statusCode = bestResp.StatusCode
	}

	if len(tsm) > 0 {
		ts.Merge(true, tsm...)
	}

	headers.StripMergeHeaders(h)
	f(ts, rlo, statusCode, w)
}