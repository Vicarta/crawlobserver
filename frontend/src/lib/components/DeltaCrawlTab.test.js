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
import { getProjectDeltaPreview } from '../api.js';

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

  it('shows v2 publication differences, actionable refetches, stability proof, and hold', async () => {
    getProjectDeltaPreview.mockResolvedValueOnce({
      baseline_session_id: 'baseline-2',
      total_candidates: 4,
      will_launch: 4,
      deferred: 1,
      launch_limit: 4,
      sitemap_published_differences: 2084,
      sitemap_actionable: 4,
      sitemap_stable_acknowledged: 2080,
      sitemap_canaries: 1,
      sitemap_deferred: 1,
      sitemap_selection: {
        published_difference_total: 2084,
        actionable_total: 4,
        stable_acknowledged_total: 2080,
        stability_older_session_id: 'raw-older',
        stability_newer_session_id: 'raw-newer',
        stability_proof_digest: 'proof-sha256',
        stability_legacy_complete_pair: true,
        publication_held: true,
      },
    });

    component = mount(DeltaCrawlTab, {
      target: document.body,
      props: { projectId: 'project-1', isAdmin: true, onerror: vi.fn() },
    });

    await vi.waitFor(() => expect(document.body.textContent).toContain('Published differences'));
    const text = document.body.textContent;
    expect(text).toContain('Actionable refetches');
    expect(text).toContain('Raw-stable acknowledged');
    expect(text).toContain('Legacy complete pair');
    expect(text).toContain('raw-older -> raw-newer');
    expect(text).toContain('proof-sha256');
    expect(text).toContain('Current Snapshot retained; raw stability is not publication evidence.');
    expect(text).not.toContain('Pending unpublished');
  });
});
