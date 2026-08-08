import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  getSessions,
  getSession,
  getCoreWebVitals,
  getExternalLinks,
  getCurrentUser,
  exportSession,
  getSessionQualityHistory,
  getSessionPageRankEvidence,
  reevaluateSessionQuality,
  subscribeProgress,
  AUTH_EXPIRED_EVENT,
  AuthError,
  resetAuthExpiredSignal,
} from './api.js';

// --- fetchJSON (tested via getSessions) ---

describe('fetchJSON', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn();
    resetAuthExpiredSignal();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('returns parsed JSON on success', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: true,
      text: () => Promise.resolve(JSON.stringify([{ ID: '1' }])),
    });
    const result = await getSessions();
    expect(result).toEqual([{ ID: '1' }]);
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/sessions', {});
  });

  it('throws with error message from JSON body on 404', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: () => Promise.resolve({ error: 'Session not found' }),
      text: () => Promise.resolve(''),
    });
    await expect(getSessions()).rejects.toThrow('Session not found');
  });

  it('throws with statusText when body is not JSON on 500', async () => {
    globalThis.fetch.mockResolvedValue({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: () => Promise.reject(new Error('parse error')),
      text: () => Promise.resolve('Internal Server Error'),
    });
    await expect(getSessions()).rejects.toThrow('Internal Server Error');
  });

  it('throws AuthError and dispatches auth-expired on 401', async () => {
    const onExpired = vi.fn();
    window.addEventListener(AUTH_EXPIRED_EVENT, onExpired);
    globalThis.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      statusText: 'Unauthorized',
      json: () => Promise.resolve({ error: 'Unauthorized' }),
      text: () => Promise.resolve(''),
    });

    await expect(getSessions()).rejects.toBeInstanceOf(AuthError);
    expect(onExpired).toHaveBeenCalledOnce();
    expect(onExpired.mock.calls[0][0].detail).toMatchObject({
      path: '/sessions',
      status: 401,
      message: 'Unauthorized',
    });
    window.removeEventListener(AUTH_EXPIRED_EVENT, onExpired);
  });

  it('does not dispatch auth-expired for suppressed auth checks', async () => {
    const onExpired = vi.fn();
    window.addEventListener(AUTH_EXPIRED_EVENT, onExpired);
    globalThis.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      statusText: 'Unauthorized',
      json: () => Promise.resolve({ error: 'Unauthorized' }),
      text: () => Promise.resolve(''),
    });

    await expect(getCurrentUser()).rejects.toBeInstanceOf(AuthError);
    expect(onExpired).not.toHaveBeenCalled();
    window.removeEventListener(AUTH_EXPIRED_EVENT, onExpired);
  });

  it('throws on network failure', async () => {
    globalThis.fetch.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(getSessions()).rejects.toThrow('Failed to fetch');
  });
});

describe('getExternalLinks', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: () => Promise.resolve('[]'),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('preserves negated source and target filters in the server-side query', async () => {
    await getExternalLinks('snapshot-1', 100, 0, {
      source_url: '!/cdn-cgi/',
      target_url: '!facebook.com',
    });

    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/sessions/snapshot-1/links?limit=100&offset=0&source_url=!%2Fcdn-cgi%2F&target_url=!facebook.com',
      {},
    );
  });
});

describe('getSession', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: () => Promise.resolve(JSON.stringify({ ID: 'current-snapshot' })),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('loads a synthetic snapshot by ID without relying on the session list', async () => {
    await expect(getSession('current-snapshot')).resolves.toEqual({ ID: 'current-snapshot' });
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/sessions/current-snapshot', {});
  });
});

describe('getCoreWebVitals', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: () => Promise.resolve(JSON.stringify({ summary: {}, pages: [], total: 0 })),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('builds the paginated, filtered and sorted lab-data report URL', async () => {
    await getCoreWebVitals('session / 1', 25, 50, 'needs_improvement', 'lcp', 'asc');

    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/sessions/session%20%2F%201/core-web-vitals?limit=25&offset=50&rating=needs_improvement&sort=lcp&order=asc',
      {},
    );
  });
});

describe('quality evidence API', () => {
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      text: () => Promise.resolve(JSON.stringify({ changed: true })),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('loads immutable quality history and latest PageRank evidence', async () => {
    await getSessionQualityHistory('session / 1');
    await getSessionPageRankEvidence('session / 1');

    expect(globalThis.fetch).toHaveBeenNthCalledWith(
      1,
      '/api/sessions/session%20%2F%201/quality/history',
      {},
    );
    expect(globalThis.fetch).toHaveBeenNthCalledWith(
      2,
      '/api/sessions/session%20%2F%201/pagerank/evidence',
      {},
    );
  });

  it('posts an explicit audited re-evaluation request with expected revisions', async () => {
    await reevaluateSessionQuality('session-1', {
      confirm: true,
      reason: 'Repair stale PageRank quality evidence',
      expected_evaluation_revision: 'quality-v1',
      expected_pagerank_evidence_revision: 'pagerank-v1',
    });

    expect(globalThis.fetch).toHaveBeenCalledWith('/api/sessions/session-1/quality/re-evaluate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        confirm: true,
        reason: 'Repair stale PageRank quality evidence',
        expected_evaluation_revision: 'quality-v1',
        expected_pagerank_evidence_revision: 'pagerank-v1',
      }),
    });
  });

  it('preserves a re-evaluation conflict status for the UI refresh path', async () => {
    globalThis.fetch.mockResolvedValueOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: () => Promise.resolve({ error: 'quality evidence revision changed' }),
      text: () => Promise.resolve(''),
    });

    await expect(
      reevaluateSessionQuality('session-1', {
        confirm: true,
        reason: 'Repair stale PageRank quality evidence',
      }),
    ).rejects.toMatchObject({ status: 409, message: 'quality evidence revision changed' });
  });
});

// --- exportSession ---

describe('exportSession', () => {
  let openSpy;

  beforeEach(() => {
    openSpy = vi.spyOn(window, 'open').mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('opens export URL without html param', () => {
    exportSession('sess1', false);
    expect(openSpy).toHaveBeenCalledWith('/api/sessions/sess1/export?include_html=false', '_blank');
  });

  it('opens export URL with html param', () => {
    exportSession('sess1', true);
    expect(openSpy).toHaveBeenCalledWith('/api/sessions/sess1/export?include_html=true', '_blank');
  });
});

// --- subscribeProgress ---

describe('subscribeProgress', () => {
  let MockEventSource;

  beforeEach(() => {
    vi.useFakeTimers();
    MockEventSource = vi.fn().mockImplementation(function (url) {
      this.url = url;
      this.onopen = null;
      this.onmessage = null;
      this.onerror = null;
      this.close = vi.fn();
      this._listeners = {};
      this.addEventListener = vi.fn((event, handler) => {
        this._listeners[event] = handler;
      });
      // Store for test access
      MockEventSource._lastInstance = this;
    });
    globalThis.EventSource = MockEventSource;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('calls onMessage with parsed data on message event', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    subscribeProgress('sess1', onMessage, onDone);

    const es = MockEventSource._lastInstance;
    es.onmessage({ data: '{"pages_crawled":42}' });

    expect(onMessage).toHaveBeenCalledWith({ pages_crawled: 42 });
  });

  it('closes and calls onDone on done event', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    subscribeProgress('sess1', onMessage, onDone);

    const es = MockEventSource._lastInstance;
    // Trigger done event
    const doneHandler = es.addEventListener.mock.calls.find((c) => c[0] === 'done')[1];
    doneHandler();

    expect(es.close).toHaveBeenCalled();
    expect(onDone).toHaveBeenCalledOnce();
  });

  it('retries with exponential backoff on error', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    subscribeProgress('sess1', onMessage, onDone);

    const es1 = MockEventSource._lastInstance;
    // Simulate error
    es1.onerror();
    expect(es1.close).toHaveBeenCalled();

    // Should not reconnect yet
    expect(MockEventSource).toHaveBeenCalledTimes(1);

    // Advance 1s — first retry delay
    vi.advanceTimersByTime(1000);
    expect(MockEventSource).toHaveBeenCalledTimes(2);

    // Second error
    const es2 = MockEventSource._lastInstance;
    es2.onerror();

    // Advance 2s — second retry delay
    vi.advanceTimersByTime(2000);
    expect(MockEventSource).toHaveBeenCalledTimes(3);
  });

  it('gives up after max retries and calls onDone', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    subscribeProgress('sess1', onMessage, onDone);

    // Trigger 11 errors (initial + 10 retries)
    for (let i = 0; i < 11; i++) {
      const es = MockEventSource._lastInstance;
      es.onerror();
      // Advance past any retry delay
      vi.advanceTimersByTime(60000);
    }

    expect(onDone).toHaveBeenCalledOnce();
  });

  it('close() cancels retry timer', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    const handle = subscribeProgress('sess1', onMessage, onDone);

    const es = MockEventSource._lastInstance;
    es.onerror(); // triggers a retry timer

    handle.close();

    // Advance past retry delay — should NOT reconnect
    vi.advanceTimersByTime(10000);
    // Only the initial connection
    expect(MockEventSource).toHaveBeenCalledTimes(1);
  });

  it('resets retry count on successful open', () => {
    const onMessage = vi.fn();
    const onDone = vi.fn();
    subscribeProgress('sess1', onMessage, onDone);

    // Error once
    const es1 = MockEventSource._lastInstance;
    es1.onerror();
    vi.advanceTimersByTime(1000);

    // Reconnect succeeds
    const es2 = MockEventSource._lastInstance;
    es2.onopen();

    // Error again — should start from 1s retry, not 2s
    es2.onerror();
    vi.advanceTimersByTime(1000);
    expect(MockEventSource).toHaveBeenCalledTimes(3);
  });
});
