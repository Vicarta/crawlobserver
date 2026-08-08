const sourceTranslationKeys = {
  internal_link: 'urlDetail.discoveryInternalLink',
  redirect_internal_link: 'urlDetail.discoveryRedirectInternalLink',
  sitemap: 'urlDetail.discoverySitemap',
  seed: 'urlDetail.discoverySeed',
  found_on: 'urlDetail.discoveryFoundOn',
  candidate: 'urlDetail.discoveryCandidate',
  unknown: 'urlDetail.discoveryUnknown',
};

export function discoverySourceTranslationKey(source) {
  return sourceTranslationKeys[source] || sourceTranslationKeys.unknown;
}

export function discoveryReferrerMeta(referrer) {
  const items = [];
  if (referrer?.link_location) items.push(referrer.link_location);
  if (referrer?.rel) items.push(`rel=${referrer.rel}`);
  if (referrer?.tag) items.push(`<${referrer.tag}>`);
  return items;
}

export function discoveryCandidateSources(discovery) {
  return Array.isArray(discovery?.candidate_sources)
    ? [...new Set(discovery.candidate_sources.filter(Boolean))]
    : [];
}
