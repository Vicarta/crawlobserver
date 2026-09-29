import { mount, unmount } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const logsAPI = vi.hoisted(() => ({
  exportLogs: vi.fn(),
  getLogs: vi.fn(),
  getOperationalEmailHistory: vi.fn(),
}));

vi.mock('../api.js', () => logsAPI);

import LogsPage from './LogsPage.svelte';

let component;

beforeEach(() => {
  logsAPI.getLogs.mockResolvedValue({ logs: [], total: 0 });
  logsAPI.getOperationalEmailHistory.mockResolvedValue({
    receipts: [
      {
        id: 1,
        session_id: 'session-12345678',
        project_name: 'DiskInternals',
        session_status: 'completed_with_errors',
        finished_at: '2026-09-29T12:00:00Z',
        event_type: 'new_page_errors',
        recipient: 'admin@example.test',
        status: 'accepted',
        summary: '2 new page error(s)',
      },
    ],
    total: 1,
    status: {
      worker_running: true,
      resend_configured: true,
      eligible_admin_count: 1,
      activated_at: '2026-09-29T11:00:00Z',
    },
  });
});

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('LogsPage operational email history', () => {
  it('shows admin status, project, provider-accepted receipt, and session navigation', async () => {
    const navigate = vi.fn();
    component = mount(LogsPage, {
      target: document.body,
      props: { currentUser: { role: 'admin' }, onerror: vi.fn(), onnavigate: navigate },
    });

    await vi.waitFor(() => expect(document.body.textContent).toContain('DiskInternals'));
    expect(document.body.textContent).toContain('Worker running');
    expect(document.body.textContent).toContain('Resend configured');
    expect(document.body.textContent).toContain('Accepted by provider');
    expect(document.body.textContent).toContain('does not confirm inbox delivery');
    const sessionLink = document.querySelector('a[href="/sessions/session-12345678/pages"]');
    expect(sessionLink).not.toBeUndefined();
    sessionLink.click();
    expect(navigate).toHaveBeenCalledWith('/sessions/session-12345678/pages');
  });

  it('does not load operational receipts for a viewer', async () => {
    component = mount(LogsPage, {
      target: document.body,
      props: { currentUser: { role: 'viewer' }, onerror: vi.fn() },
    });
    await vi.waitFor(() => expect(logsAPI.getLogs).toHaveBeenCalled());
    expect(logsAPI.getOperationalEmailHistory).not.toHaveBeenCalled();
    expect(document.querySelector('.operational-email')).toBeNull();
  });

  it('shows empty history and missing mail configuration to admins', async () => {
    logsAPI.getOperationalEmailHistory.mockResolvedValueOnce({
      receipts: [],
      total: 0,
      status: {
        worker_running: false,
        resend_configured: false,
        eligible_admin_count: 0,
        activated_at: '2026-09-29T11:00:00Z',
      },
    });
    component = mount(LogsPage, {
      target: document.body,
      props: { currentUser: { role: 'admin' }, onerror: vi.fn() },
    });
    await vi.waitFor(() => expect(document.body.textContent).toContain('Worker stopped'));
    expect(document.body.textContent).toContain('No operational email receipts yet.');
    expect(document.body.textContent).toContain('Worker stopped');
    expect(document.body.textContent).toContain('Resend not configured');
    expect(document.body.textContent).toContain('Eligible admins: 0');
    expect(document.querySelector('.logs-empty')?.closest('.operational-email-table-wrap')).toBeNull();
  });

  it('shows loading instead of an empty-history claim before the initial request resolves', async () => {
    let resolveHistory;
    logsAPI.getOperationalEmailHistory.mockReturnValueOnce(new Promise((resolve) => {
      resolveHistory = resolve;
    }));
    component = mount(LogsPage, {
      target: document.body,
      props: { currentUser: { role: 'admin' }, onerror: vi.fn() },
    });
    expect(document.body.textContent).toContain('Loading...');
    expect(document.body.textContent).not.toContain('No operational email receipts yet.');
    expect(document.querySelector('.logs-empty')?.closest('.operational-email-table-wrap')).toBeNull();
    resolveHistory({ receipts: [], total: 0, status: null });
    await vi.waitFor(() => expect(document.body.textContent).toContain('No operational email receipts yet.'));
    expect(document.querySelector('.logs-empty')?.closest('.operational-email-table-wrap')).toBeNull();
  });

  it('clears stale receipt status when refreshing history fails', async () => {
    component = mount(LogsPage, {
      target: document.body,
      props: { currentUser: { role: 'admin' }, onerror: vi.fn() },
    });
    await vi.waitFor(() => expect(document.body.textContent).toContain('DiskInternals'));
    logsAPI.getOperationalEmailHistory.mockRejectedValueOnce(new Error('temporary API failure'));
    document.querySelector('.operational-email-heading button').click();
    await vi.waitFor(() => expect(document.body.textContent).toContain('Operational email history is unavailable'));
    expect(document.body.textContent).not.toContain('DiskInternals');
    expect(document.body.textContent).not.toContain('Worker running');
    expect(document.body.textContent).not.toContain('No operational email receipts yet.');
    expect(document.querySelector('.email-status-accepted')).toBeNull();
    expect(document.querySelector('.operational-email-table-wrap')).toBeNull();
  });
});
