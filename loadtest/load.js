// Continuous read + write load for the whole rollout.
//
// Every API request is written to the log as one line:
//   R,<unix_ms>,<read|write>,<status>,<latency_ms>,<ok 0|1>,<X-App-Version>
// and `zdm report` slices those lines into the rollout steps recorded by
// scripts/e2e.sh. A request counts as failed unless the status AND the body
// are right: reads must return the exact deterministic name of the seeded id,
// writes must echo the name and return a new id.
//
// The load runs until the control URL answers 200 (scripts/e2e.sh creates the
// file); then every VU stops issuing requests, so k6 can be interrupted with
// nothing in flight and no request is cut off half-way.
import http from 'k6/http';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const CONTROL = __ENV.CONTROL_URL || 'http://127.0.0.1:8099/stop';
const SEED_ROWS = parseInt(__ENV.SEED_ROWS || '200000', 10);
const READ_RATE = parseInt(__ENV.READ_RATE || '40', 10);
const WRITE_RATE = parseInt(__ENV.WRITE_RATE || '10', 10);
const DURATION = __ENV.DURATION || '45m';
const OUT = __ENV.OUT_DIR || '/out';

const apiRequests = new Counter('api_requests');
const apiErrors = new Counter('api_errors');

export const options = {
  scenarios: {
    reads: {
      executor: 'constant-arrival-rate', exec: 'read', rate: READ_RATE, timeUnit: '1s',
      duration: DURATION, preAllocatedVUs: 20, maxVUs: 200, gracefulStop: '10s',
    },
    writes: {
      executor: 'constant-arrival-rate', exec: 'write', rate: WRITE_RATE, timeUnit: '1s',
      duration: DURATION, preAllocatedVUs: 10, maxVUs: 100, gracefulStop: '10s',
    },
  },
  // No thresholds here: `zdm report` evaluates the gates per rollout step.
};

// Keep in sync with internal/names/seed.go (TestSeedNameMatchesLoadTest).
const SEED_FIRST = [
  'Ada', 'Alan', 'Barbara', 'Charles', 'Dennis', 'Donald', 'Edsger', 'Frances',
  'Grace', 'Hedy', 'John', 'Katherine', 'Ken', 'Leslie', 'Linus', 'Margaret',
  'Niklaus', 'Radia', 'Rob', 'Shafi', 'Tim', 'Vint', 'Whitfield', 'Yukihiro',
];
const SEED_LAST = [
  'Lovelace', 'Turing', 'Liskov', 'Shannon', 'Ritchie', 'Knuth', 'Dijkstra', 'Allen',
  'Hopper', 'Lamarr', 'McCarthy', 'Johnson', 'Thompson', 'Lamport', 'Torvalds', 'Hamilton',
  'Wirth', 'Perlman', 'Pike', 'Goldwasser', 'Berners Lee', 'Cerf', 'Diffie', 'Matsumoto',
  'van Rossum', 'Kernighan', 'Stroustrup',
];

export function seedName(i) {
  const first = SEED_FIRST[i % SEED_FIRST.length];
  if (i % 50 === 0) {
    return first;
  }
  return first + ' ' + SEED_LAST[Math.floor(i / SEED_FIRST.length) % SEED_LAST.length];
}

// Per-VU stop flag, refreshed from the control URL at most every 2 s.
let stopped = false;
let lastPoll = 0;
function shouldStop() {
  if (stopped) {
    return true;
  }
  const now = Date.now();
  if (now - lastPoll > 2000) {
    lastPoll = now;
    const r = http.get(CONTROL, { tags: { name: 'control' }, timeout: '1s' });
    stopped = r.status === 200;
  }
  return stopped;
}

function latency(res) {
  const t = res.timings;
  // Client-observed latency including connection setup (reconnects after a
  // pod sends "Connection: close" while draining are part of the cost).
  return t.blocked + t.connecting + t.tls_handshaking + t.duration;
}

function record(op, t0, res, ok) {
  apiRequests.add(1);
  apiErrors.add(ok ? 0 : 1);
  const version = (res.headers && res.headers['X-App-Version']) || '-';
  console.log(`R,${t0},${op},${res.status},${latency(res).toFixed(2)},${ok ? 1 : 0},${version}`);
}

function body(res) {
  try {
    return res.json();
  } catch (e) {
    return null;
  }
}

export function read() {
  if (shouldStop()) {
    return;
  }
  const id = 1 + Math.floor(Math.random() * SEED_ROWS);
  const t0 = Date.now();
  const res = http.get(`${BASE}/users/${id}`, { tags: { name: 'GET /users/{id}' }, timeout: '10s' });
  const b = res.status === 200 ? body(res) : null;
  record('read', t0, res, b !== null && b.id === id && b.name === seedName(id));
}

export function write() {
  if (shouldStop()) {
    return;
  }
  const name = `Load${exec.vu.idInTest} W${exec.scenario.iterationInTest}`;
  const t0 = Date.now();
  const res = http.post(`${BASE}/users`, JSON.stringify({ name }), {
    headers: { 'Content-Type': 'application/json' }, tags: { name: 'POST /users' }, timeout: '10s',
  });
  const b = res.status === 201 ? body(res) : null;
  record('write', t0, res, b !== null && b.name === name && b.id > SEED_ROWS);
}

function count(data, metric) {
  const m = data.metrics[metric];
  return m && m.values && m.values.count ? m.values.count : 0;
}

export function handleSummary(data) {
  const summary = {
    api_requests: count(data, 'api_requests'),
    api_errors: count(data, 'api_errors'),
    dropped_iterations: count(data, 'dropped_iterations'),
  };
  return {
    [`${OUT}/k6-summary.json`]: JSON.stringify(summary, null, 2),
    stdout: `k6 summary: ${JSON.stringify(summary)}\n`,
  };
}
