import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
    stages: [
        { duration: '30s', target: 10 },
    ],
    thresholds: {
        http_req_duration: ['p(95)<500'], // 95% of requests must be below 500ms
        'checks': ['rate>0.99'],          // More than 99% of requests must pass checks
    },
};

function getBaseEndpoint() {
    const url = __ENV.TRICKSTER_BASE_ENDPOINT;
    return url;
}

export default function () {
    const QUERY_ENDPOINT = '/api/v1/query';
    const PROMQL_QUERY = 'rate(process_cpu_seconds_total[5m])';
    const fullUrl = `${getBaseEndpoint()}${QUERY_ENDPOINT}?query=${encodeURIComponent(PROMQL_QUERY)}`;
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
