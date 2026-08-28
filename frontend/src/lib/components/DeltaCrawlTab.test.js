import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  getProjectDeltaSettings: vi.fn(async () => ({
    enabled: true,
    sitemap_changed_limit: 0,
    sitemap_canary_count: 50,
    max_candidates_per_run: 80,
  })),
  getProjectDeltaPreview: vi.fn(async () => ({
    baseline_session_id: 'baseline-1',
    total_candidates: 4,
    will_launch: 2,
    deferred: 2,
    launch_limit: 2,
    sitemap_events: 1,
    sitemap_pending_unpublished: 1,
    sitemap_canaries: 1,
    sitemap_deferred: 2,
    sitemap_selection: { selection_complete: false },
    held_publication_reason: 'Sitemap publication held: 2 changed event candidates are deferred.',
  })),
  updateProjectDeltaSettings: vi.fn(),
  addProjectDeltaManualURLs: vi.fn(),
  runProjectDelta: vi.fn(),
}));

import DeltaCrawlTab from './DeltaCrawlTab.svelte';

let component;

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
});

describe('DeltaCrawlTab sitemap selection', () => {
  it('shows pending, canary, deferred, and held-publication state', async () => {
    component = mount(DeltaCrawlTab, {
      target: document.body,
      props: { projectId: 'project-1', isAdmin: true, onerror: vi.fn() },
    });

    await vi.waitFor(() => expect(document.body.textContent).toContain('Changed events'));
    const text = document.body.textContent;
    expect(text).toContain('Pending unpublished');
    expect(text).toContain('Canaries');
    expect(text).toContain('Sitemap deferred');
    expect(text).toContain('Sitemap publication held');
    expect(text).toContain('Changed sitemap cap (0 = all)');
    expect(text).toContain('Rotating sitemap canaries');
  });
});
