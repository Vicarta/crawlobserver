import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ getStructuredDataReport: vi.fn() }));
vi.mock('../../api.js', () => api);

import StructuredDataReport from './StructuredDataReport.svelte';

let component;

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('StructuredDataReport', () => {
  it('renders technical observations and links rows to URL detail', async () => {
    api.getStructuredDataReport.mockResolvedValue({
      summary: { total: 2, not_checked: 1, no_markup: 0, issues: 1, clean: 0 },
      total: 1,
      pages: [
        {
          url: 'https://example.test/product',
          status: 'issues',
          coverage: 'json_ld_syntax',
          sources: ['static'],
          formats: ['json-ld'],
          types: ['Product'],
          json_ld_entity_count: 1,
          diagnostic_count: 2,
          truncated: true,
          diagnostics: [
            {
              source: 'static',
              format: 'json-ld',
              code: 'json_ld_invalid_json',
              block_index: 2,
              byte_start: 18,
              byte_end: 19,
            },
            {
              source: 'static',
              format: 'json-ld',
              code: 'json_ld_invalid_json',
              block_index: 3,
              byte_start: 24,
              byte_end: 24,
            },
          ],
        },
      ],
    });
    const onnavigate = vi.fn();
    component = mount(StructuredDataReport, {
      target: document.body,
      props: { sessionId: 'sess-1', onnavigate },
    });

    await vi.waitFor(() => expect(document.querySelector('.sd-table tbody tr')).not.toBeNull());
    expect(document.body.textContent).toContain('Product');
    expect(document.body.textContent).toContain('json_ld_invalid_json');
    expect(document.body.textContent).toContain('bytes 18-19');
    expect(document.body.textContent).toContain('EOF at byte 24');
    expect(document.body.textContent).toContain('Static');
    expect(document.body.textContent).toContain('Incomplete scan');

    document.querySelector('.sd-url a').click();
    expect(onnavigate).toHaveBeenCalledWith('/sessions/sess-1/url/https%3A%2F%2Fexample.test%2Fproduct');
  });

  it('applies status and type filters through the report API', async () => {
    api.getStructuredDataReport.mockResolvedValue({
      summary: { total: 1, not_checked: 0, no_markup: 0, issues: 1, clean: 0 },
      total: 1,
      pages: [],
    });
    component = mount(StructuredDataReport, {
      target: document.body,
      props: { sessionId: 'sess-1' },
    });
    await vi.waitFor(() => expect(api.getStructuredDataReport).toHaveBeenCalledTimes(1));

    const status = document.querySelector('.sd-filters select');
    status.value = 'issues';
    status.dispatchEvent(new Event('change', { bubbles: true }));
    await vi.waitFor(() => expect(api.getStructuredDataReport).toHaveBeenCalledTimes(2));
    expect(api.getStructuredDataReport.mock.calls[1][1]).toMatchObject({ status: 'issues', offset: 0 });

    const type = document.querySelector('.sd-type-filter input');
    type.value = 'Product';
    type.dispatchEvent(new Event('input', { bubbles: true }));
    type.form.requestSubmit();
    await vi.waitFor(() => expect(api.getStructuredDataReport).toHaveBeenCalledTimes(3));
    expect(api.getStructuredDataReport.mock.calls[2][1]).toMatchObject({ status: 'issues', type: 'Product' });
  });
});
