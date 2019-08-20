package prometheus

import (
	"net/http"

	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/origins/prometheus"
	"github.com/go-kit/kit/log"
)

type PrometheusClient struct {
	client *prometheus.Client
}

func NewPrometheusClient(originConfig *config.OriginConfig, logger log.Logger) (*PrometheusClient, error) {
	c, err := registration.GetCache(originConfig.CacheName)
	if err != nil {
		return nil, err
	}
	t := &PrometheusClient{
		client: prometheus.NewClient(originConfig.Type, originConfig, c, logger),
	}
	return t, nil
}

func (t *PrometheusClient) Query(w http.ResponseWriter, r *http.Request) {
	t.client.QueryHandler(w, r)
}

func (t *PrometheusClient) QueryRange(w http.ResponseWriter, r *http.Request) {
	t.client.QueryRangeHandler(w, r)
}

func (t *PrometheusClient) Series(w http.ResponseWriter, r *http.Request) {
	t.client.SeriesHandler(w, r)
}

func (t *PrometheusClient) Labels(w http.ResponseWriter, r *http.Request) {
	t.client.ProxyHandler(w, r)
}

func (t *PrometheusClient) LabelValues(w http.ResponseWriter, r *http.Request) {
	t.client.ProxyHandler(w, r)
}
