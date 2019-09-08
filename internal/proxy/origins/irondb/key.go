package irondb

import (
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the user request.
func (c Client) DeriveCacheKey(r *model.Request, extra string) string {
	switch r.HandlerName {
	case "CAQLHandler":
		return c.caqlHandlerDeriveCacheKey(r, extra)
	case "FindHandler":
		return c.findHandlerDeriveCacheKey(r, extra)
	case "HistogramHandler":
		return c.histogramHandlerDeriveCacheKey(r, extra)
	case "RawHandler":
		return c.rawHandlerDeriveCacheKey(r, extra)
	case "RollupHandler":
		return c.rollupHandlerDeriveCacheKey(r, extra)
	case "FetchHandler":
		return c.fetchHandlerDeriveCacheKey(r, extra)
	case "TextHandler":
		return c.textHandlerDeriveCacheKey(r, extra)
	default:
		return md5.Checksum(r.URL.RawPath + r.URL.RawQuery)
	}
}
