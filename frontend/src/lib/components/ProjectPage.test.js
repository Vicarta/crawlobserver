import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  getSessionsPaginated: vi.fn(async () => ({
    sessions: [
      {
        ID: 'session-proven',
        SeedURLs: ['http://www.example.test'],
        Status: 'completed',
        ProjectID: 'project-1',
        PagesCrawled: 80,
        StartedAt: '2026-08-28T10:00:00Z',
        effective_origin: 'https://www.example.test',
        effective_origin_state: 'proven',
      },
      {
        ID: 'session-unavailable',
        SeedURLs: ['http://legacy.example.test'],
        Status: 'completed',
        ProjectID: 'project-1',
        PagesCrawled: 1,
        StartedAt: '2026-08-27T10:00:00Z',
        effective_origin: '',
        effective_origin_state: 'unavailable',
      },
      {
        ID: 'session-direct',
        SeedURLs: ['https://direct.example.test/'],
        Status: 'completed',
        ProjectID: 'project-1',
        PagesCrawled: 1,
        StartedAt: '2026-08-27T09:00:00Z',
        effective_origin: 'https://direct.example.test',
        effective_origin_state: 'proven',
      },
      {
        ID: 'session-ambiguous',
        SeedURLs: ['https://mixed.example.test'],
        Status: 'completed',
        ProjectID: 'project-1',
        PagesCrawled: 2,
        StartedAt: '2026-08-26T10:00:00Z',
        effective_origin: '',
        effective_origin_state: 'ambiguous',
      },
      {
        ID: 'session-long',
        SeedURLs: ['http://very-long-audit-seed-name-that-must-wrap.example.test/path'],
        Status: 'completed',
        ProjectID: 'project-1',
        PagesCrawled: 1,
        StartedAt: '2026-08-25T10:00:00Z',
        effective_origin: 'https://very-long-operational-origin-name-that-must-wrap.example.test',
        effective_origin_state: 'proven',
      },
    ],
    total: 5,
  })),
  getProjectCurrentSnapshot: vi.fn(async () => null),
  getProviderConnections: vi.fn(async () => []),
  renameProject: vi.fn(),
  deleteProject: vi.fn(),
  deleteProjectWithSessions: vi.fn(),
  disassociateSession: vi.fn(),
  getSessionQuality: vi.fn(),
  getSessionQualityHistory: vi.fn(),
  getSessionPageRankEvidence: vi.fn(),
  reevaluateSessionQuality: vi.fn(),
}));

import ProjectPage from './ProjectPage.svelte';

let component;

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
});

describe('ProjectPage operational origin', () => {
  it('shows proven effective origin without rewriting raw seed provenance', async () => {
    component = mount(ProjectPage, {
      target: document.body,
      props: {
        project: { id: 'project-1', name: 'Example' },
        currentUser: { role: 'admin' },
        onerror: vi.fn(),
      },
    });

    await vi.waitFor(() => expect(document.body.textContent).toContain('https://www.example.test'));

    const text = document.body.textContent;
    expect(text).toContain('Raw seed: http://www.example.test');
    expect(text).toContain('Origin unavailable');
    expect(text).toContain('Raw seed: http://legacy.example.test');
    expect(text).toContain('https://direct.example.test');
    expect(text).toContain('Raw seed: https://direct.example.test/');
    expect(text).toContain('Origin ambiguous');
    expect(text).toContain('Raw seed: https://mixed.example.test');
    expect(text).toContain('https://very-long-operational-origin-name-that-must-wrap.example.test');
    expect(text).toContain('Raw seed: http://very-long-audit-seed-name-that-must-wrap.example.test/path');
    expect(document.querySelector('.session-origin-cell')).not.toBeNull();
  });
});
