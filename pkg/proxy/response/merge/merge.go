package merge

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/proxy/request"
)

// ResponseGate is a Request/ResponseWriter Pair that must be handled in its entirety
// before its respective response pool can be merged.
type ResponseGate struct {
	http.ResponseWriter
	Request   *http.Request
	Resources *request.Resources
	body      []byte
	header    http.Header
}

// ResponseGates represents a slice of type *ResponseGate
type ResponseGates []*ResponseGate

// NewResponseGate provides a new ResponseGate object
func NewResponseGate(w http.ResponseWriter, r *http.Request, rsc *request.Resources) *ResponseGate {
	rg := &ResponseGate{ResponseWriter: w, Request: r, Resources: rsc}
	if w != nil {
		rg.header = w.Header().Clone()
	}
	return rg
}

// Header returns the ResponseGate's Header map
func (rg *ResponseGate) Header() http.Header {
	return rg.header
}

// WriteHeader is not used with a ResponseGate
func (rg *ResponseGate) WriteHeader(i int) {
}

// Body returns the stored body for merging
func (rg *ResponseGate) Body() []byte {
	return rg.body
}

// Write is not used with a ResponseGate
func (rg *ResponseGate) Write(b []byte) (int, error) {

	l := len(b)

	if l == 0 {
		return 0, nil
	}

	if rg.body == nil {
		rg.body = make([]byte, l)
		copy(rg.body, b)
	} else {
		rg.body = append(rg.body, b...)
	}

	return len(b), nil
}
