import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => {
  class AuthError extends Error {}
  return {
    AUTH_EXPIRED_EVENT: 'crawlobserver:auth-expired',
    AuthError,
    acceptInvitation: vi.fn(),
    applyUpdate: vi.fn(),
    createBackup: vi.fn(),
    createProject: vi.fn(),
    deleteSession: vi.fn(),
    getCurrentUser: vi.fn(),
    getGlobalStats: vi.fn(),
    getInvitation: vi.fn(),
    getProgress: vi.fn(),
    getProjectCurrentSnapshot: vi.fn(),
    getProjects: vi.fn(),
    getSession: vi.fn(),
    getSessionStorage: vi.fn(),
    getSessions: vi.fn(),
    getSetupStatus: vi.fn(),
    getStats: vi.fn(),
    getStorageStats: vi.fn(),
    getSystemStats: vi.fn(),
    getTelemetry: vi.fn(),
    getUpdateStatus: vi.fn(),
    logout: vi.fn(),
    requestLoginCode: vi.fn(),
    stopCrawl: vi.fn(),
    subscribeProgress: vi.fn(),
    verifyLoginCode: vi.fn(),
  };
});

vi.mock('./lib/api.js', () => api);
vi.mock('./lib/theme.js', () => ({
  applyTheme: vi.fn(),
  listenColorScheme: vi.fn(),
  loadThemeFromServer: vi.fn(),
  saveDarkMode: vi.fn(),
}));
vi.mock('./lib/telemetry.js', () => ({
  disableTelemetry: vi.fn(),
  initTelemetry: vi.fn(),
  trackEvent: vi.fn(),
  trackPageView: vi.fn(),
}));
vi.mock('./lib/router.js', () => ({
  parseRoute: vi.fn(() => ({ page: 'home' })),
  pushURL: vi.fn(),
}));

import App from './App.svelte';
import { loadThemeFromServer } from './lib/theme.js';

let component;

function seedApp() {
  loadThemeFromServer.mockResolvedValue({
    theme: { app_name: 'CrawlObserver', accent_color: '#0f766e', mode: 'light' },
    darkMode: false,
  });
  api.getSetupStatus.mockResolvedValue({ setup_complete: true, telemetry_asked: true });
  api.getCurrentUser.mockResolvedValue({ id: 'existing-user', role: 'viewer' });
  api.getInvitation.mockResolvedValue({ email: 'person@example.test' });
  api.acceptInvitation.mockResolvedValue({ id: 'invited-user', role: 'viewer' });
  api.getProjects.mockResolvedValue([]);
  api.getSessions.mockResolvedValue([]);
  api.getSessionStorage.mockResolvedValue({});
  api.getTelemetry.mockResolvedValue({ enabled: false, session_recording: false });
}

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  window.history.replaceState({}, '', '/');
  vi.clearAllMocks();
});

describe('App invitation routing', () => {
  it('keeps an invitation visible over a preserved session, then clears its query after acceptance', async () => {
    seedApp();
    window.history.replaceState({}, '', '/?invite=valid-token');
    component = mount(App, { target: document.body });

    await vi.waitFor(() => expect(document.body.textContent).toContain('Accept your invitation'));
    expect(api.getCurrentUser).toHaveBeenCalledOnce();

    document.querySelector('.login-submit').click();

    await vi.waitFor(() => {
      expect(api.acceptInvitation).toHaveBeenCalledWith('valid-token');
      expect(window.location.search).toBe('');
      expect(document.body.textContent).not.toContain('Accept your invitation');
    });
  });
});
