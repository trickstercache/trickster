import http from 'k6/http';
import { check, sleep } from 'k6';

// Simulates a Grafana dashboard: multiple panels issuing query_range requests
// with a floating time window (e.g., "last 6 hours") on a periodic refresh.

const RANGE_SECONDS = parseInt(__ENV.RANGE_SECONDS || '21600'); // default 6h
const STEP_SECONDS  = parseInt(__ENV.STEP_SECONDS  || '15');    // default 15s
const REFRESH_INTERVAL = parseFloat(__ENV.REFRESH_INTERVAL || '5'); // seconds between refreshes

// Panels — each entry is a PromQL query, like a real dashboard with several panels.
// Override PROMQL_QUERY to set a single query, or PANEL_QUERIES for a JSON array.
function getPanelQueries() {
    if (__ENV.PANEL_QUERIES) {
        return JSON.parse(__ENV.PANEL_QUERIES);
    }
    const single = __ENV.PROMQL_QUERY || 'rate(process_cpu_seconds_total[5m])';
    return [single];
}

function getBaseEndpoint() {
    return __ENV.TRICKSTER_BASE_ENDPOINT || 'http://localhost:8480/prom1';
}

const opts = {
    thresholds: {
        http_req_duration: ['p(95)<1000'],
        'checks': ['rate>0.95'],
    },
};

if (__ENV.K6_RATE) {
    const rate     = parseInt(__ENV.K6_RATE);
    const duration = __ENV.K6_DURATION || '2m';
    const maxVUs   = parseInt(__ENV.K6_MAX_VUS || '500');
    opts.scenarios = {
        constant_rate: {
            executor: 'constant-arrival-rate',
            rate: rate,
            timeUnit: '1s',
            duration: duration,
            preAllocatedVUs: Math.min(maxVUs, Math.ceil(rate / 2)),
            maxVUs: maxVUs,
        },
    };
}

export const options = opts;

export default function () {
    const base = getBaseEndpoint();
    const queries = getPanelQueries();

    // Floating window: end = now, start = now - range
    const endEpoch   = Math.floor(Date.now() / 1000);
    const startEpoch = endEpoch - RANGE_SECONDS;

    // Fire all panel queries (Grafana sends them in parallel on each refresh)
    const requests = queries.map((q) => ({
        method: 'GET',
        url: `${base}/api/v1/query_range`
            + `?query=${encodeURIComponent(q)}`
            + `&start=${startEpoch}`
            + `&end=${endEpoch}`
            + `&step=${STEP_SECONDS}`
            + `&time=${endEpoch}`,
    }));

    const responses = http.batch(requests);

    for (let i = 0; i < responses.length; i++) {
        check(responses[i], {
            'is status 200': (r) => r.status === 200,
            'response is success': (r) => {
                try {
                    return JSON.parse(r.body).status === 'success';
                } catch (e) {
                    return false;
                }
            },
        });
    }

    // In rate mode the executor controls pacing; only sleep in iterations mode.
    if (!__ENV.K6_RATE) {
        sleep(REFRESH_INTERVAL);
    }
}
