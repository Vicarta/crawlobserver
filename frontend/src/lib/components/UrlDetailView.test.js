import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  getPageDetail: vi.fn(async () => ({
    page: {
      URL: 'https://example.com/final/',
      FinalURL: 'https://example.com/final/',
      StatusCode: 404,
      ContentType: 'text/html',
      BodySize: 12000,
      FetchDurationMs: 120,
      Depth: 6,
      CrawledAt: '2026-08-03T00:00:00Z',
      Headers: {},
      H1: ['Missing'],
      H2: [],
      H3: [],
      H4: [],
      H5: [],
      H6: [],
      HeadingOutline: [{ Level: 1, Text: 'Missing' }],
      RedirectChain: [],
      SchemaTypes: [],
      Hreflang: [],
      WordCount: 10,
      ImagesCount: 0,
      ImagesNoAlt: 0,
      InternalLinksOut: 0,
      ExternalLinksOut: 0,
    },
    links: { out_links: [], in_links: [], out_links_count: 0, in_links_count: 0 },
    discovery: {
      availability: 'derived',
      primary_source: 'redirect_internal_link',
      detail: '1 internal referring source via redirect',
      found_on: '',
      is_seed: false,
      is_in_sitemap: false,
      candidate_sources: [],
      referrers_count: 1,
      referrers: [
        {
          source_url: 'https://example.com/source',
          target_url: 'https://example.com/old',
          redirect_url: 'https://example.com/old',
          anchor_text: 'Old article',
          link_location: 'body',
          tag: 'a',
          via_redirect: true,
        },
      ],
    },
  })),
  getBacklinksTop: vi.fn(),
  getStructuredData: vi.fn(),
  getHreflangValidation: vi.fn(),
  computeHreflangValidation: vi.fn(),
  getGSCPageQueries: vi.fn(async () => ({ rows: [], total: 0 })),
}));

import UrlDetailView from './UrlDetailView.svelte';

let component;

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
});

describe('UrlDetailView discovery evidence', () => {
  it('renders redirect provenance before GSC Ranking Keywords', async () => {
    component = mount(UrlDetailView, {
      target: document.body,
      props: {
        sessionId: 'session-1',
        projectId: 'project-1',
        url: 'https://example.com/final/',
      },
    });

    await vi.waitFor(() => {
      expect(document.body.textContent).toContain('URL discovered from');
    });

    const text = document.body.textContent;
    expect(text).toContain('Internal link via redirect');
    expect(text).toContain('https://example.com/source');
    expect(text).toContain('Old article');
    expect(text).toContain('https://example.com/old');
    expect(text.indexOf('URL discovered from')).toBeLessThan(text.indexOf('GSC Ranking Keywords'));
  });
});
