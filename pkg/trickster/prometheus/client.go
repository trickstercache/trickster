package prometheus

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/origins/prometheus"
)

type processFunc func(*TricksterClient, http.ResponseWriter, *http.Request)

type TricksterClient struct {
	client       *prometheus.Client
	apiPrefixMap map[string]processFunc
}

func NewTricksterClient(originConfig *config.OriginConfig) (*TricksterClient, error) {
	c, err := registration.GetCache(originConfig.CacheName)
	if err != nil {
		return nil, err
	}
	t := &TricksterClient{
		client: prometheus.NewClient(originConfig.Type, originConfig, c),
		apiPrefixMap: map[string]processFunc{
			originConfig.PathPrefix + originConfig.APIPath + "/query_range?": processQueryRange,
			originConfig.PathPrefix + originConfig.APIPath + "/query?":       processQuery,
			originConfig.PathPrefix + originConfig.APIPath + "/series?":      processSeries,
		},
	}
	return t, nil
}

func (t *TricksterClient) Process(w http.ResponseWriter, r *http.Request) {
	for prefix, f := range t.apiPrefixMap {
		if strings.HasPrefix(r.URL.String(), prefix) {
			f(t, w, r)
			return
		}
	}
	t.client.ProxyHandler(w, r)
}

func processQuery(t *TricksterClient, w http.ResponseWriter, r *http.Request) {
	t.client.QueryHandler(w, r)
}

func processQueryRange(t *TricksterClient, w http.ResponseWriter, r *http.Request) {
	t.client.QueryRangeHandler(w, r)
}

func processSeries(t *TricksterClient, w http.ResponseWriter, r *http.Request) {
	t.client.SeriesHandler(w, r)
}
