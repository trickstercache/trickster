package proxy_test

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func TestListenerConnectionLimitWorks(t *testing.T) {
	metrics.Init() // For some reason I need to call it specifically

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "hello!")
	}
	es := httptest.NewServer(http.HandlerFunc(handler))
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	tt := []struct {
		Name             string
		ListenPort       int
		ConnectionsLimit int
		Clients          int
		expectedErr      string
	}{
		{
			"Without connection limit",
			34001,
			0,
			1,
			"",
		},
		{
			"With connection limit of 10",
			34002,
			10,
			10,
			"",
		},
		{
			"With connection limit of 1, but with 10 clients",
			34003,
			1,
			10,
			"Get http://localhost:34003/: net/http: request canceled (Client.Timeout exceeded while awaiting headers)",
		},
	}

	http.DefaultClient.Timeout = 100 * time.Millisecond

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			l, err := proxy.NewListener("", tc.ListenPort, tc.ConnectionsLimit)
			defer l.Close()

			go func() {
				http.Serve(l, routing.Router)
			}()

			if err != nil {
				t.Fatalf("failed to create listener: %s", err)
			}

			for i := 0; i < tc.Clients; i++ {
				r, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d/", tc.ListenPort), nil)
				if err != nil {
					t.Fatalf("failed to create request: %s", err)
				}
				res, err := http.DefaultClient.Do(r)
				if err != nil {
					if fmt.Sprintf("%s", err) != tc.expectedErr {
						t.Fatalf("unexpected error when executing request: %s", err)
					}
					continue
				}
				defer func() {
					io.Copy(ioutil.Discard, res.Body)
					res.Body.Close()
				}()
			}

		})
	}
}
