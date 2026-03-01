import http from 'k6/http';
import { check, sleep } from 'k6';

// Simulates a Grafana dashboard: multiple panels issuing query_range requests
// with a floating time window (e.g., "last 3 hours") on a periodic refresh.

const RANGE_SECONDS = parseInt(__ENV.RANGE_SECONDS || '10800'); // default 3h
const STEP_SECONDS  = parseInt(__ENV.STEP_SECONDS  || '15');    // default 15s
const REFRESH_INTERVAL = parseFloat(__ENV.REFRESH_INTERVAL || '10'); // seconds between refreshes

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

export const options = {
    stages: [
        { duration: '15s', target: 10 },  // ramp up
        { duration: '60s', target: 10 },   // sustained load
        { duration: '10s', target: 0 },   // ramp down
    ],
    thresholds: {
        http_req_duration: ['p(95)<1000'],
        'checks': ['rate>0.95'],
    },
};

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
            + `&step=${STEP_SECONDS}`,
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

    // Simulate dashboard refresh interval
    sleep(REFRESH_INTERVAL);
}
