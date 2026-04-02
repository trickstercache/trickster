import http from 'k6/http';
import { check } from 'k6';

const opts = {
    thresholds: {
        http_req_duration: ['p(95)<500'], // 95% of requests must be below 500ms
        'checks': ['rate>0.99'],          // More than 99% of requests must pass checks
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

function getBaseEndpoint() {
    return __ENV.TRICKSTER_BASE_ENDPOINT || 'http://localhost:8480/prom1';
}

function getQuery() {
    return __ENV.PROMQL_QUERY || 'rate(process_cpu_seconds_total[5m])';
}

export default function () {
    const QUERY_ENDPOINT = '/api/v1/query';
    const fullUrl = `${getBaseEndpoint()}${QUERY_ENDPOINT}?query=${encodeURIComponent(getQuery())}`;
    const res = http.get(fullUrl);
    check(res, {
        'is status 200': (r) => r.status === 200,
    });
    check(res, {
        'response status is success': (r) => {
            try {
                return JSON.parse(r.body).status === 'success';
            } catch (e) {
                return false;
            }
        },
    });
}
