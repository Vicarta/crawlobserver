import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  discoveryCandidateSources,
  discoveryReferrerMeta,
  discoverySourceTranslationKey,
} from './pageDiscovery.js';

describe('page discovery presentation', () => {
  it('maps redirect evidence to a distinct source label', () => {
    expect(discoverySourceTranslationKey('redirect_internal_link')).toBe(
      'urlDetail.discoveryRedirectInternalLink',
    );
    expect(discoverySourceTranslationKey('unexpected')).toBe('urlDetail.discoveryUnknown');
  });

  it('keeps compact referrer metadata and removes duplicate candidates', () => {
    expect(discoveryReferrerMeta({ link_location: 'body', rel: 'nofollow', tag: 'a' })).toEqual([
      'body',
      'rel=nofollow',
      '<a>',
    ]);
    expect(
      discoveryCandidateSources({ candidate_sources: ['problem_pages', 'problem_pages', 'stale'] }),
    ).toEqual(['problem_pages', 'stale']);
  });

  it('renders the discovery block before GSC Ranking Keywords', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/lib/components/UrlDetailView.svelte'),
      'utf8',
    );
    expect(source.indexOf("t('urlDetail.discoveryTitle')")).toBeGreaterThan(-1);
    expect(source.indexOf("t('urlDetail.discoveryTitle')")).toBeLessThan(
      source.indexOf("t('urlDetail.gscKeywords')"),
    );
  });
});
