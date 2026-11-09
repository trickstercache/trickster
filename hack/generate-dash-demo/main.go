/*
 * Copyright 2026 The Trickster Authors
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

// Command generate-dash-demo drives dashboard queries, web browsing and MoQ viewing through the demo Trickster
// in trickster-generate-dash-demo.yml, so the Grafana dashboards in deploy/grafana have data to show.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/jackc/pgx/v5"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	bePromEast   = "prom-demo-01.us-east-1a"
	bePromWest   = "prom-demo-01.us-west-2"
	beTSMGlobal  = "tsm-demo-global.us-east-1"
	beTSMMerge   = "tsm-demo-merge.global"
	beClickHouse = "clickhouse-demo-01.us-east-1"
	beDruid      = "druid-demo-01.us-east-1"
	beGraphite   = "graphite-demo-01.us-east-1"
	beWWW        = "www-demo-01.us-east-1"
	beStatic     = "static-demo-01.us-east-1"
	beMoQRelay   = "moqrelay-demo-01.us-east-1"

	moqNamespace   = "live"
	moqTrack       = "video"
	implementation = "trickster-generate-dash-demo"
	keepAlive      = 15 * time.Second
	urgency        = 3

	// a group per second, opening with a keyframe-sized object
	keyObjectSize   = 24 << 10
	deltaObjectSize = 2 << 10
	deltaObjects    = 14

	// the load wave: a two-hour swell with a faster ripple on top
	wavePeriod   = 2 * time.Hour
	ripplePeriod = 13 * time.Minute
	minLoad      = 0.3

	hnRange           = "Range"
	hnIfModifiedSince = "If-Modified-Since"
	hnContentType     = "Content-Type"
	hvJSON            = "application/json"
	lastModified      = "Wed, 01 Jan 2020 00:00:00 GMT"
)

var channels = []string{"newsroom-hd", "stadium-cam-1", "stadium-cam-2", "weather-radar"}

// a channel's share of new viewers
var channelWeights = []float64{0.45, 0.3, 0.15, 0.1}

type options struct {
	httpURL, httpsURL, ingestURL, pgDSN, ca string
	interval, report                        time.Duration
	browsers, viewers                       int
}

type stats struct {
	queries, queryErrors          atomic.Int64
	requests, requestErrors       atomic.Int64
	bytes                         atomic.Int64
	browsers, viewers             atomic.Int64
	objects, published            atomic.Int64
	viewerErrors, publishFailures atomic.Int64
}

func main() {
	var o options
	flag.StringVar(&o.httpURL, "http", "http://127.0.0.1:18980", "the demo Trickster's HTTP listener")
	flag.StringVar(&o.httpsURL, "https", "https://127.0.0.1:18983", "the demo Trickster's HTTPS and HTTP/3 listener")
	flag.StringVar(&o.ingestURL, "moq-ingest", "moqt://127.0.0.1:18990/", "the demo Trickster's MoQ ingest listener")
	// Trickster caches only simple-protocol statements, so pgx is told not to prepare them
	flag.StringVar(&o.pgDSN, "pg", "postgres://grafana_ro:trickster-dev-grafana@127.0.0.1:18988/trickster"+
		"?sslmode=disable&default_query_exec_mode=simple_protocol", "the demo Trickster's PostgreSQL listener")
	flag.StringVar(&o.ca, "ca", "docs/developer/environment/certs/trickster-dev.crt",
		"a PEM certificate trusted for the demo Trickster's TLS listeners")
	flag.DurationVar(&o.interval, "interval", 5*time.Second, "the pause between dashboard refreshes")
	flag.IntVar(&o.browsers, "browsers", 16, "simulated browsers at the peak of the load wave")
	flag.IntVar(&o.viewers, "viewers", 10, "MoQ viewers at the peak of the load wave")
	flag.DurationVar(&o.report, "report", time.Minute, "how often to log a traffic summary")
	flag.Parse()

	if err := run(o); err != nil {
		logf("%v", err)
		os.Exit(1)
	}
}

func run(o options) error {
	tc, err := tlsConfig(o.ca)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := &stats{}
	var wg sync.WaitGroup
	wg.Go(func() { dashboards(ctx, o, st) })
	wg.Go(func() { browsers(ctx, o, st) })
	wg.Go(func() { publisher(ctx, o, tc, st) })
	wg.Go(func() { viewers(ctx, o, tc, st) })
	wg.Go(func() { reporter(ctx, o.report, st) })
	logf("generating demo traffic through %s; Ctrl-C stops", o.httpURL)
	wg.Wait()
	return nil
}

// logf writes a timestamped line to stderr
func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format(time.DateTime)+" "+format+"\n", a...)
}

func tlsConfig(ca string) (*tls.Config, error) {
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, fmt.Errorf("reading -ca (run make dev-certs): %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", ca)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, nil
}

func newRand() *weaktest.Rand {
	n := uint64(time.Now().UnixNano()) // #nosec G115 -- the clock is past the epoch
	return weaktest.NewRand(n, n>>7^0x9e3779b97f4a7c15)
}

// load is the share of peak traffic at t, between minLoad and 1
func load(t time.Time) float64 {
	s := float64(t.UnixNano())
	swell := math.Sin(2 * math.Pi * s / float64(wavePeriod))
	ripple := math.Sin(2 * math.Pi * s / float64(ripplePeriod))
	v := 0.5 + 0.38*swell + 0.12*ripple
	return minLoad + (1-minLoad)*math.Max(0, math.Min(1, v))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func jitter(r *weaktest.Rand, lo, hi time.Duration) time.Duration {
	return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
}

func reporter(ctx context.Context, every time.Duration, st *stats) {
	for sleep(ctx, every) {
		logf("queries %d (%d failed) | web requests %d (%d failed), %.1f MB | browsers %d | viewers %d, "+
			"%d objects received (%d failed sessions) | groups published %d (%d failed)",
			st.queries.Load(), st.queryErrors.Load(), st.requests.Load(), st.requestErrors.Load(),
			float64(st.bytes.Load())/1e6, st.browsers.Load(), st.viewers.Load(), st.objects.Load(),
			st.viewerErrors.Load(), st.published.Load(), st.publishFailures.Load())
	}
}

// --- dashboards ------------------------------------------------------------------------------------------------

// a dashboard's time range for each refresh: mostly the last hour, now and then a wider look back
var ranges = []time.Duration{
	time.Hour, time.Hour, time.Hour, 6 * time.Hour, time.Hour, time.Hour, 3 * time.Hour,
	time.Hour, 24 * time.Hour, time.Hour,
}

type query struct {
	name string
	do   func(ctx context.Context, start, end time.Time, step time.Duration) error
}

func dashboards(ctx context.Context, o options, st *stats) {
	c := &http.Client{Timeout: 30 * time.Second}
	pg := &pgConn{dsn: o.pgDSN}
	defer pg.close()
	queries := timeSeriesQueries(o.httpURL, c, pg)
	logged := map[string]bool{}
	for i := 0; ; i++ {
		end := time.Now()
		rng := ranges[i%len(ranges)]
		step := stepFor(rng)
		for _, q := range queries {
			if ctx.Err() != nil {
				return
			}
			qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := q.do(qctx, end.Add(-rng), end, step)
			cancel()
			st.queries.Add(1)
			if err != nil && ctx.Err() == nil {
				st.queryErrors.Add(1)
				// a failing query is logged when it starts failing and when it recovers
				if !logged[q.name] {
					logf("query %s failed: %v", q.name, err)
				}
				logged[q.name] = true
			} else if logged[q.name] {
				logf("query %s recovered", q.name)
				delete(logged, q.name)
			}
		}
		if !sleep(ctx, o.interval) {
			return
		}
	}
}

// stepFor picks a Grafana-like step: about 300 points across the range, in whole multiples of 15s
func stepFor(rng time.Duration) time.Duration {
	const grain = 15 * time.Second
	return max(grain, (rng/300+grain-1)/grain*grain)
}

func timeSeriesQueries(base string, c *http.Client, pg *pgConn) []query {
	promRange := func(backend, expr string) query {
		return query{name: backend + " " + expr, do: func(ctx context.Context, start, end time.Time, step time.Duration) error {
			v := url.Values{
				"query": {expr}, "start": {unix(start)}, "end": {unix(end)},
				"step": {strconv.Itoa(int(step.Seconds()))},
			}
			return get(ctx, c, base+"/"+backend+"/api/v1/query_range?"+v.Encode(), nil)
		}}
	}
	promInstant := func(backend, expr string) query {
		return query{name: backend + " instant " + expr, do: func(ctx context.Context, _, end time.Time, _ time.Duration) error {
			v := url.Values{"query": {expr}, "time": {unix(end)}}
			return get(ctx, c, base+"/"+backend+"/api/v1/query?"+v.Encode(), nil)
		}}
	}
	clickhouse := func(sql string) query {
		return query{name: beClickHouse + " " + sql[:40], do: func(ctx context.Context, start, end time.Time, step time.Duration) error {
			s := strconv.Itoa(int(step.Seconds()))
			q := strings.NewReplacer("$step", s, "$from", unix(start), "$to", unix(end)).Replace(sql)
			return get(ctx, c, base+"/"+beClickHouse+"/?"+url.Values{"query": {q}}.Encode(), nil)
		}}
	}
	druid := func(sql string) query {
		return query{name: beDruid + " " + sql[:40], do: func(ctx context.Context, start, end time.Time, step time.Duration) error {
			q := strings.NewReplacer("$period", isoPeriod(step), "$from", millis(start), "$to", millis(end)).Replace(sql)
			body := fmt.Sprintf(`{"query":%q,"resultFormat":"array"}`, q)
			return post(ctx, c, base+"/"+beDruid+"/druid/v2/sql", hvJSON, body)
		}}
	}
	graphite := func(target string) query {
		return query{name: beGraphite + " " + target, do: func(ctx context.Context, start, end time.Time, _ time.Duration) error {
			v := url.Values{
				"target": {target}, "from": {unix(start)}, "until": {unix(end)}, "format": {"json"},
				"maxDataPoints": {"600"},
			}
			return get(ctx, c, base+"/"+beGraphite+"/render?"+v.Encode(), nil)
		}}
	}
	postgres := func(sql string) query {
		return query{name: "pg-demo-01.us-east-1 " + sql[:40], do: func(ctx context.Context, start, end time.Time, step time.Duration) error {
			q := strings.NewReplacer("$step", strconv.Itoa(int(step.Seconds()))+"s",
				"$from", start.UTC().Format(time.RFC3339), "$to", end.UTC().Format(time.RFC3339)).Replace(sql)
			return pg.query(ctx, q)
		}}
	}
	return []query{
		promRange(bePromEast, `sum by (job) (rate(prometheus_http_requests_total[5m]))`),
		promRange(bePromEast, `sum by (instance) (rate(process_cpu_seconds_total[5m]))`),
		promInstant(bePromEast, `up`),
		promRange(bePromWest, `sum(rate(trips_fares_dollars_total[5m]))`),
		promRange(beTSMGlobal, `sum by (code) (rate(prometheus_http_requests_total[5m]))`),
		promRange(beTSMGlobal, `sum by (job) (up)`),
		promRange(beTSMMerge, `sum(rate(trips_total[5m]))`),
		promRange(beTSMMerge, `sum(trips_in_progress)`),
		postgres(`SELECT time_bucket('$step', pickup_datetime) AS "time", count(*) AS trips FROM trips ` +
			`WHERE pickup_datetime >= '$from' AND pickup_datetime < '$to' GROUP BY 1 ORDER BY 1`),
		postgres(`SELECT time_bucket('$step', pickup_datetime) AS "time", avg(fare_amount) AS avg_fare, ` +
			`avg(tip_amount) AS avg_tip FROM trips WHERE pickup_datetime >= '$from' AND pickup_datetime < '$to' ` +
			`GROUP BY 1 ORDER BY 1`),
		clickhouse(`SELECT intDiv(toUInt32(pickup_datetime), $step) * $step * 1000 AS t, count() AS trips ` +
			`FROM default.trips WHERE pickup_datetime >= toDateTime($from) AND pickup_datetime < toDateTime($to) ` +
			`GROUP BY t ORDER BY t FORMAT JSON`),
		clickhouse(`SELECT intDiv(toUInt32(pickup_datetime), $step) * $step * 1000 AS t, ` +
			`100 * countIf(payment_type != 'CSH') / count() AS card_use_rate FROM default.trips ` +
			`WHERE pickup_datetime >= toDateTime($from) AND pickup_datetime < toDateTime($to) GROUP BY t ORDER BY t ` +
			`FORMAT JSON`),
		druid(`SELECT TIME_FLOOR(__time, '$period') AS bucket, COUNT(*) AS trips FROM "trips" ` +
			`WHERE __time >= MILLIS_TO_TIMESTAMP($from) AND __time < MILLIS_TO_TIMESTAMP($to) GROUP BY 1 ORDER BY 1`),
		graphite(`alias(sumSeries(dev.fast.requests.*.count), 'requests')`),
		graphite(`aliasByNode(dev.medium.orders.*.count, 3)`),
	}
}

func unix(t time.Time) string   { return strconv.FormatInt(t.Unix(), 10) }
func millis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func isoPeriod(step time.Duration) string {
	if step >= time.Hour && step%time.Hour == 0 {
		return "PT" + strconv.Itoa(int(step.Hours())) + "H"
	}
	if step >= time.Minute && step%time.Minute == 0 {
		return "PT" + strconv.Itoa(int(step.Minutes())) + "M"
	}
	return "PT" + strconv.Itoa(int(step.Seconds())) + "S"
}

func get(ctx context.Context, c *http.Client, u string, h http.Header) error {
	_, err := do(ctx, c, http.MethodGet, u, "", "", h)
	return err
}

func post(ctx context.Context, c *http.Client, u, contentType, body string) error {
	_, err := do(ctx, c, http.MethodPost, u, contentType, body, nil)
	return err
}

// do sends a request and reads the whole response, returning its size; a 4xx or 5xx is an error
func do(ctx context.Context, c *http.Client, method, u, contentType, body string,
	h http.Header,
) (int64, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	maps.Copy(req.Header, h)
	if contentType != "" {
		req.Header.Set(hnContentType, contentType)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return n, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return n, fmt.Errorf("status %d", resp.StatusCode)
	}
	return n, nil
}

// pgConn keeps one PostgreSQL session open across refreshes, as a Grafana data source does
type pgConn struct {
	dsn  string
	conn *pgx.Conn
}

func (p *pgConn) query(ctx context.Context, sql string) error {
	if p.conn == nil {
		c, err := pgx.Connect(ctx, p.dsn)
		if err != nil {
			return err
		}
		p.conn = c
	}
	rows, err := p.conn.Query(ctx, sql)
	if err != nil {
		p.close()
		return err
	}
	for rows.Next() {
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		p.close()
	}
	return err
}

func (p *pgConn) close() {
	if p.conn != nil {
		_ = p.conn.Close(context.Background())
		p.conn = nil
	}
}

// --- browsers --------------------------------------------------------------------------------------------------

type object struct {
	path   string
	size   int
	maxAge int
}

// catalog is a site's objects, most popular first
type catalog struct {
	pages, assets, api, video []object
}

func newCatalog(r *weaktest.Rand) *catalog {
	c := &catalog{}
	add := func(dst *[]object, n int, dir, ext string, lo, hi, maxAge int) {
		for i := range n {
			*dst = append(*dst, object{
				path: fmt.Sprintf("/%s/%s-%03d.%s", dir, dir, i, ext), size: lo + r.IntN(hi-lo+1), maxAge: maxAge,
			})
		}
	}
	add(&c.pages, 40, "pages", "html", 8<<10, 60<<10, 60)
	add(&c.assets, 60, "js", "js", 20<<10, 300<<10, 3600)
	add(&c.assets, 40, "css", "css", 10<<10, 120<<10, 3600)
	add(&c.assets, 160, "img", "webp", 4<<10, 400<<10, 86400)
	add(&c.assets, 8, "fonts", "woff2", 30<<10, 120<<10, 604800)
	add(&c.api, 30, "api/v2", "json", 1<<10, 24<<10, 5)
	add(&c.video, 120, "vod/segments", "m4s", 256<<10, 1<<20, 600)
	// popularity is by position, so mix the kinds together
	r.Shuffle(len(c.assets), func(i, j int) { c.assets[i], c.assets[j] = c.assets[j], c.assets[i] })
	return c
}

// pick favors the front of a list, as a few objects draw most requests
func pick(r *weaktest.Rand, objs []object) object {
	u := r.Float64()
	return objs[int(float64(len(objs))*u*u*u)]
}

func browsers(ctx context.Context, o options, st *stats) {
	r := newRand()
	cat := newCatalog(r)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		want := int64(math.Round(float64(o.browsers) * load(time.Now())))
		for st.browsers.Load() < want {
			st.browsers.Add(1)
			seed := r.Uint64()
			wg.Go(func() {
				defer st.browsers.Add(-1)
				browse(ctx, o, cat, weaktest.NewRand(seed, seed>>3), st)
			})
		}
		if !sleep(ctx, jitter(r, time.Second, 4*time.Second)) {
			return
		}
	}
}

// browse is one visitor: a few pages with their assets and API calls, sometimes a video, then gone
func browse(ctx context.Context, o options, cat *catalog, r *weaktest.Rand, st *stats) {
	tr := &http.Transport{MaxIdleConnsPerHost: 6, IdleConnTimeout: 30 * time.Second}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	base := o.httpURL + "/" + beWWW
	until := time.Now().Add(jitter(r, 30*time.Second, 5*time.Minute))
	fetch := func(obj object, h http.Header, extra string) {
		u := fmt.Sprintf("%s%s?size=%d&max-age=%d%s", base, obj.path, obj.size, obj.maxAge, extra)
		n, err := do(ctx, c, http.MethodGet, u, "", "", h)
		st.requests.Add(1)
		st.bytes.Add(n)
		if err != nil && ctx.Err() == nil {
			st.requestErrors.Add(1)
		}
	}
	if r.IntN(4) == 0 {
		n, err := do(ctx, c, http.MethodGet, o.httpURL+"/"+beStatic+"/", "", "", nil)
		st.requests.Add(1)
		st.bytes.Add(n)
		if err != nil && ctx.Err() == nil {
			st.requestErrors.Add(1)
		}
	}
	for time.Now().Before(until) && ctx.Err() == nil {
		var h http.Header
		if r.IntN(10) == 0 {
			// a returning visitor revalidates what it has
			h = http.Header{hnIfModifiedSince: {lastModified}}
		}
		// the cache key leaves out the query string, so a broken link or a failing call needs a path of its own
		switch n := r.IntN(1000); {
		case n < 15:
			fetch(object{path: fmt.Sprintf("/pages/retired-%03d.html", r.IntN(40)), size: 1 << 10}, nil, "&status=404")
		case n < 18:
			fetch(object{path: fmt.Sprintf("/api/v2/checkout-%03d.json", r.IntN(500)), size: 1 << 10}, nil,
				"&status=500")
		}
		fetch(pick(r, cat.pages), h, "")
		for range 3 + r.IntN(10) {
			fetch(pick(r, cat.assets), h, "")
		}
		for range r.IntN(4) {
			fetch(pick(r, cat.api), nil, "")
		}
		if r.IntN(6) == 0 {
			watch(ctx, cat, r, fetch)
		}
		if !sleep(ctx, jitter(r, time.Second, 8*time.Second)) {
			return
		}
	}
}

// watch plays a run of consecutive video segments, each in two byte ranges as a player buffers
func watch(ctx context.Context, cat *catalog, r *weaktest.Rand, fetch func(object, http.Header, string)) {
	first := int(float64(len(cat.video)) * r.Float64() * r.Float64())
	for i := first; i < min(len(cat.video), first+2+r.IntN(10)) && ctx.Err() == nil; i++ {
		seg := cat.video[i]
		half := seg.size / 2
		fetch(seg, http.Header{hnRange: {fmt.Sprintf("bytes=0-%d", half-1)}}, "")
		fetch(seg, http.Header{hnRange: {fmt.Sprintf("bytes=%d-", half)}}, "")
		if !sleep(ctx, jitter(r, 500*time.Millisecond, 2*time.Second)) {
			return
		}
	}
}

// --- MoQ -------------------------------------------------------------------------------------------------------

func trackName(channel string) message.FullTrackName {
	return message.FullTrackName{
		Namespace: wire.Namespace{[]byte(moqNamespace), []byte(channel)}, Name: []byte(moqTrack),
	}
}

func dialMoQ(ctx context.Context, rawURL string, tc *tls.Config, h session.Handler) (*session.Session, func(), error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	tc = tc.Clone()
	tc.ServerName = u.Hostname()
	qc := &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true, KeepAlivePeriod: keepAlive}
	setup := message.Setup{Implementation: implementation}
	var (
		conn    session.Conn
		c       *codec.Codec
		ok, wt  bool
		closeFn = func() {}
	)
	switch u.Scheme {
	case "moqt":
		tc.NextProtos = codec.ALPNs(codec.ClientOffer)
		qconn, err := quicdial.DialAddr(ctx, u.Host, tc, qc)
		if err != nil {
			return nil, nil, err
		}
		if c, ok = codec.ForALPN(qconn.ConnectionState().TLS.NegotiatedProtocol); !ok {
			_ = qconn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "")
			return nil, nil, errors.New("the relay negotiated no supported draft")
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		setup.Path, setup.HasPath, setup.Authority, setup.HasAuthority = path, true, u.Host, true
		conn = session.FromQUIC(qconn)
	case "https":
		tc.NextProtos = []string{http3.NextProtoH3}
		tr := &webtransport.Transport{
			TLSClientConfig: tc, QUICConfig: qc, ApplicationProtocols: codec.ALPNs(codec.ClientOffer),
			DialAddr: quicdial.DialAddr,
		}
		closeFn = func() { _ = tr.Close() }
		_, ws, err := tr.Dial(ctx, u.String(), nil)
		if err != nil {
			closeFn()
			return nil, nil, err
		}
		if c, ok = codec.ForALPN(ws.SessionState().ApplicationProtocol); !ok {
			_ = ws.CloseWithError(webtransport.WTALPNErrorCode, "")
			closeFn()
			return nil, nil, errors.New("the relay negotiated no supported draft")
		}
		conn, wt = session.FromWebTransport(ws), true
	default:
		return nil, nil, fmt.Errorf("unsupported MoQ URL scheme %q", u.Scheme)
	}
	s, err := session.New(conn, session.Options{
		Role: session.RoleClient, Codec: c, Setup: setup, Handler: h, WebTransport: wt,
	})
	if err != nil {
		_ = conn.CloseWithError(uint64(message.CodeInternalError), "")
		closeFn()
		return nil, nil, err
	}
	if err = s.Start(ctx); err != nil {
		closeFn()
		return nil, nil, err
	}
	return s, func() {
		_ = s.Close(message.CodeNoError, "")
		closeFn()
	}, nil
}

func answer(ctx context.Context, req *session.Request, want message.Kind) error {
	m, err := req.Response(ctx)
	if err != nil {
		return err
	}
	if re, refused := m.(*message.RequestError); refused {
		return fmt.Errorf("refused: %s (code %#x)", re.Reason, uint64(re.Code))
	}
	if m.Kind() != want {
		return fmt.Errorf("unexpected answer %s", m.Kind())
	}
	return nil
}

// channelPublisher serves the relay's subscriptions to the live channels it publishes
type channelPublisher struct {
	mu   sync.Mutex
	subs map[*session.Request]string
}

func (p *channelPublisher) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *channelPublisher) HandleRequest(r *session.Request) {
	r.SetHandlers(p, nil)
	sub, ok := r.Message().(*message.Subscribe)
	if !ok {
		_ = r.Reject(message.RequestNotSupported, "only SUBSCRIBE is served", 0)
		return
	}
	for _, ch := range channels {
		if sub.Track.Equal(trackName(ch)) {
			if r.Send(&message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()}) == nil {
				p.mu.Lock()
				p.subs[r] = ch
				p.mu.Unlock()
			}
			return
		}
	}
	_ = r.Reject(message.RequestDoesNotExist, "not published here", 0)
}

func (p *channelPublisher) HandleMessage(*session.Request, message.Message) {}

func (p *channelPublisher) HandleClose(r *session.Request, _ error) {
	p.mu.Lock()
	delete(p.subs, r)
	p.mu.Unlock()
}

// publisher keeps the live channels publishing into the ingest relay, reconnecting when the session ends
func publisher(ctx context.Context, o options, tc *tls.Config, st *stats) {
	for ctx.Err() == nil {
		if err := publish(ctx, o.ingestURL, tc, st); err != nil && ctx.Err() == nil {
			st.publishFailures.Add(1)
			logf("MoQ publisher: %v; reconnecting", err)
		}
		if !sleep(ctx, 5*time.Second) {
			return
		}
	}
}

func publish(ctx context.Context, ingest string, tc *tls.Config, st *stats) error {
	p := &channelPublisher{subs: make(map[*session.Request]string)}
	s, closeFn, err := dialMoQ(ctx, ingest, tc, p)
	if err != nil {
		return err
	}
	defer closeFn()
	req, err := s.Open(ctx, &message.PublishNamespace{Prefix: wire.Namespace{[]byte(moqNamespace)}}, p, nil)
	if err != nil {
		return err
	}
	if err = answer(ctx, req, message.KindRequestOK); err != nil {
		return err
	}
	logf("MoQ publisher: publishing %d channels under %s/ into %s", len(channels), moqNamespace, ingest)
	r := newRand()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.Done():
			return s.Err()
		case now := <-tick.C:
			// the group ID is the clock's second, so viewers can name recent groups to fetch
			group := uint64(now.Unix()) // #nosec G115 -- the clock is past the epoch
			p.mu.Lock()
			subs := make(map[*session.Request]string, len(p.subs))
			maps.Copy(subs, p.subs)
			p.mu.Unlock()
			for req := range subs {
				if err := sendGroup(ctx, req, group, r); err != nil {
					st.publishFailures.Add(1)
				} else {
					st.published.Add(1)
				}
			}
		}
	}
}

func sendGroup(ctx context.Context, req *session.Request, group uint64, r *weaktest.Rand) error {
	out, err := req.OpenSubgroup(ctx, message.SubgroupHeader{
		Group: group, Priority: 1, EndOfGroup: true, FirstObject: true,
	}, urgency, false)
	if err != nil {
		return err
	}
	buf := make([]byte, keyObjectSize)
	for i := range deltaObjects + 1 {
		size := deltaObjectSize
		if i == 0 {
			size = keyObjectSize
		}
		b := buf[:size]
		for j := 0; j+8 <= len(b); j += 8 {
			binary.LittleEndian.PutUint64(b[j:], r.Uint64())
		}
		if err = out.WriteObject(&message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(size)}); err != nil {
			return err
		}
		if _, err = out.Write(b); err != nil {
			return err
		}
	}
	return out.Close()
}

// viewerSink counts the objects a viewer receives
type viewerSink struct {
	st *stats
}

func (v *viewerSink) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	for {
		if _, err := in.Next(); err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, in); err != nil {
			return
		}
		v.st.objects.Add(1)
	}
}

func (v *viewerSink) HandleDatagram(*session.Request, *message.Datagram) { v.st.objects.Add(1) }

func (v *viewerSink) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	for {
		obj, err := in.Next()
		if err != nil {
			return
		}
		if obj.EndOfRange != message.RangeEndNone {
			continue
		}
		if _, err = io.Copy(io.Discard, in); err != nil {
			return
		}
		v.st.objects.Add(1)
	}
}

func (v *viewerSink) HandleMessage(*session.Request, message.Message) {}

func (v *viewerSink) HandleClose(*session.Request, error) {}

func viewers(ctx context.Context, o options, tc *tls.Config, st *stats) {
	r := newRand()
	relay := o.httpsURL + "/" + beMoQRelay + "/"
	var wg sync.WaitGroup
	defer wg.Wait()
	// give the publisher a head start, so the first viewers find their channels live
	if !sleep(ctx, 5*time.Second) {
		return
	}
	for {
		want := int64(math.Round(float64(o.viewers) * load(time.Now())))
		for st.viewers.Load() < want {
			st.viewers.Add(1)
			ch := weighted(r, channels, channelWeights)
			stay := jitter(r, 20*time.Second, 6*time.Minute)
			catchUp := r.IntN(5) == 0
			wg.Go(func() {
				defer st.viewers.Add(-1)
				if err := view(ctx, relay, tc, ch, stay, catchUp, st); err != nil && ctx.Err() == nil {
					st.viewerErrors.Add(1)
				}
			})
		}
		if !sleep(ctx, jitter(r, 2*time.Second, 6*time.Second)) {
			return
		}
	}
}

func weighted(r *weaktest.Rand, names []string, weights []float64) string {
	u := r.Float64()
	for i, w := range weights {
		if u < w {
			return names[i]
		}
		u -= w
	}
	return names[len(names)-1]
}

// view watches one channel for a while; a viewer catching up first fetches the last few seconds
func view(ctx context.Context, relay string, tc *tls.Config, channel string, stay time.Duration, catchUp bool,
	st *stats,
) error {
	s, closeFn, err := dialMoQ(ctx, relay, tc, nil)
	if err != nil {
		return err
	}
	defer closeFn()
	sink := &viewerSink{st: st}
	ft := trackName(channel)
	if catchUp {
		now := uint64(time.Now().Unix()) // #nosec G115 -- the clock is past the epoch
		var p message.Params
		p.SetLocationFilter(message.LocationFilter{
			Type: message.FilterAbsoluteStartGroupEnd, StartGroup: now - 8, EndGroupDelta: 5,
		})
		if req, err := s.Open(ctx, &message.Fetch{Track: ft, Params: p}, sink, sink); err == nil {
			_ = answer(ctx, req, message.KindFetchOK)
		}
	}
	req, err := s.Open(ctx, &message.Subscribe{Track: ft}, sink, sink)
	if err != nil {
		return err
	}
	if err = answer(ctx, req, message.KindSubscribeOK); err != nil {
		return err
	}
	t := time.NewTimer(stay)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-s.Done():
		return s.Err()
	}
	return nil
}
