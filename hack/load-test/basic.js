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

export default function () {
    const TRICKSTER_URL = 'http://localhost:8480/prom1';
    const QUERY_ENDPOINT = '/api/v1/query';
    const PROMQL_QUERY = 'rate(process_cpu_seconds_total[5m])';
    const fullUrl = `${TRICKSTER_URL}${QUERY_ENDPOINT}?query=${encodeURIComponent(PROMQL_QUERY)}`;
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
    sleep(1);
}
