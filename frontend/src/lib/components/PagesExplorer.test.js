import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

const pagesAPI = vi.hoisted(() => ({
  buildApiPath: vi.fn((path, params) => `${path}?${new URLSearchParams(params)}`),
  deletePages: vi.fn(),
  getPageIssues: vi.fn(),
  getPages: vi.fn(),
  getRedirectPages: vi.fn(),
  rescanPages: vi.fn(),
}));

const pageUtils = vi.hoisted(() => ({ downloadCSV: vi.fn() }));

vi.mock('../api.js', () => pagesAPI);
vi.mock('../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...pageUtils,
}));

import PagesExplorer from './PagesExplorer.svelte';

let component;

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('PagesExplorer unified search', () => {
  it('restores search state, sends it with page filters, and exposes the stored HTML limitation', async () => {
    pagesAPI.getPages.mockResolvedValue([]);
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', initialFilters: { q: 'body text', regex: '1' } },
    });

    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalled());
    expect(pagesAPI.getPages).toHaveBeenCalledWith(
      'sess-1',
      100,
      0,
      { q: 'body text', regex: '1' },
      '',
      '',
    );
    expect(document.querySelector('[aria-label="Search pages"]')?.value).toBe('body text');
    expect(document.querySelector('.pages-search-regex input').checked).toBe(true);
    expect(document.querySelector('.pages-search-note').textContent).toContain(
      'Crawls without store_html match URL, title, and meta description.',
    );
  });

  it('applies changed query and regex state through the existing Pages URL', async () => {
    let resolveSearch;
    pagesAPI.getPages
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(new Promise((resolve) => (resolveSearch = resolve)));
    const onpushurl = vi.fn();
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', onpushurl },
    });
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(1));

    const input = document.querySelector('[aria-label="Search pages"]');
    input.value = ' fox ';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    const regex = document.querySelector('.pages-search-regex input');
    regex.checked = true;
    regex.dispatchEvent(new Event('change', { bubbles: true }));
    input.form.requestSubmit();

    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(2));
    expect(document.querySelector('.pages-search-status').textContent.trim()).toBe('Searching…');
    expect(pagesAPI.getPages).toHaveBeenLastCalledWith(
      'sess-1',
      100,
      0,
      { q: ' fox ', regex: '1' },
      '',
      '',
    );
    expect(onpushurl).toHaveBeenLastCalledWith('/sessions/sess-1/pages/all?q=+fox+&regex=1');
    resolveSearch([]);
    await vi.waitFor(() =>
      expect(document.querySelector('.pages-search-status').textContent.trim()).toBe(''),
    );
  });

  it('uses the submitted search for pagination and CSV export while newer input is a draft', async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) => ({
      URL: `https://example.test/${index}`,
      StatusCode: 200,
      PageType: 'html',
    }));
    pagesAPI.getPages.mockResolvedValueOnce(firstPage).mockResolvedValue([]);
    const onpushurl = vi.fn();
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', initialFilters: { q: 'applied', regex: '1' }, onpushurl },
    });
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(1));
    expect(document.querySelector('.pagination')).not.toBeNull();

    const input = document.querySelector('[aria-label="Search pages"]');
    input.value = 'draft';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector('.pagination button:last-child').click();

    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(2));
    expect(pagesAPI.getPages).toHaveBeenLastCalledWith(
      'sess-1',
      100,
      100,
      { q: 'applied', regex: '1' },
      '',
      '',
    );
    expect(onpushurl).toHaveBeenLastCalledWith(
      '/sessions/sess-1/pages/all?q=applied&regex=1&offset=100',
    );

    document.querySelector('.export-toggle').click();
    await vi.waitFor(() => expect(document.querySelector('.export-menu-item')).not.toBeNull());
    document.querySelector('.export-menu-item').click();
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(3));
    expect(pagesAPI.getPages).toHaveBeenLastCalledWith('sess-1', 100, 0, {
      q: 'applied',
      regex: '1',
    });
    await vi.waitFor(() => expect(pageUtils.downloadCSV).toHaveBeenCalled());
  });

  it('keeps the initial filters and view across every CSV batch', async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) => ({
      URL: `https://example.test/${index}`,
      StatusCode: 200,
      PageType: 'html',
    }));
    pagesAPI.getPages
      .mockResolvedValueOnce([])
      .mockImplementationOnce((sessionId, limit, offset, filters) => {
        expect([sessionId, limit, offset, filters]).toEqual([
          'sess-1',
          100,
          0,
          { q: 'before', regex: '1' },
        ]);
        const input = document.querySelector('[aria-label="Search pages"]');
        input.value = 'after';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.form.requestSubmit();
        [...document.querySelectorAll('.pr-subview-btn')]
          .find((button) => button.textContent.trim() === 'Titles')
          .click();
        return Promise.resolve(firstPage);
      })
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([]);
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', initialFilters: { q: 'before', regex: '1' } },
    });
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(1));

    document.querySelector('.export-toggle').click();
    await vi.waitFor(() => expect(document.querySelector('.export-menu-item')).not.toBeNull());
    document.querySelector('.export-menu-item').click();

    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(5));
    expect(pagesAPI.getPages.mock.calls[4]).toEqual([
      'sess-1',
      100,
      100,
      { q: 'before', regex: '1' },
    ]);
    await vi.waitFor(() =>
      expect(pageUtils.downloadCSV).toHaveBeenCalledWith(
        'pages-all.csv',
        expect.any(Array),
        expect.any(Array),
        expect.any(Array),
      ),
    );
  });

  it('ignores stale page responses after a newer search and shows then clears invalid regex locally', async () => {
    let resolveInitial;
    pagesAPI.getPages
      .mockReturnValueOnce(new Promise((resolve) => (resolveInitial = resolve)))
      .mockResolvedValueOnce(
        Array.from({ length: 100 }, (_, index) => ({
          URL: `https://example.test/prior-${index}`,
          StatusCode: 200,
          PageType: 'html',
        })),
      )
      .mockRejectedValueOnce(
        Object.assign(new Error('invalid RE2 regular expression'), { status: 400 }),
      )
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(
        Object.assign(new Error('page search query too long (max 500 bytes)'), { status: 400 }),
      );
    const onerror = vi.fn();
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', onerror },
    });
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(1));

    const input = document.querySelector('[aria-label="Search pages"]');
    input.value = 'new';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.form.requestSubmit();
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(2));
    await vi.waitFor(() =>
      expect(document.querySelector('.data-table-resizable tbody').children).toHaveLength(100),
    );
    expect(document.querySelector('.pagination')).not.toBeNull();
    resolveInitial([{ URL: 'https://example.test/stale', StatusCode: 200, PageType: 'html' }]);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(
      document.querySelector('.data-table-resizable tbody tr:first-child .cell-url')?.textContent,
    ).toContain('prior-0');

    const regex = document.querySelector('.pages-search-regex input');
    regex.checked = true;
    regex.dispatchEvent(new Event('change', { bubbles: true }));
    input.value = '[';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.form.requestSubmit();
    await vi.waitFor(() =>
      expect(document.querySelector('.pages-search-error')?.textContent).toBe(
        'invalid RE2 regular expression',
      ),
    );
    expect(onerror).not.toHaveBeenCalled();
    expect(document.querySelector('.data-table-resizable tbody').children).toHaveLength(0);
    expect(document.querySelector('.pagination')).toBeNull();
    expect(document.querySelector('.export-toggle').disabled).toBe(true);

    document.querySelector('.btn-filter-clear').click();
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(4));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(document.querySelector('.pages-search-error')).toBeNull();
    expect(document.querySelector('[aria-label="Search pages"]').value).toBe('');
    expect(document.querySelector('.export-toggle').disabled).toBe(false);

    input.value = 'x'.repeat(501);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.form.requestSubmit();
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(5));
    await vi.waitFor(() =>
      expect(document.querySelector('.pages-search-error')?.textContent).toBe(
        'page search query too long (max 500 bytes)',
      ),
    );
    expect(document.querySelector('.data-table-resizable tbody').children).toHaveLength(0);
    expect(document.querySelector('.export-toggle').disabled).toBe(true);
  });

  it('reports CSV fetch errors without rejecting the export click handler', async () => {
    pagesAPI.getPages
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error('export unavailable'));
    const onerror = vi.fn();
    component = mount(PagesExplorer, {
      target: document.body,
      props: { sessionId: 'sess-1', onerror },
    });
    await vi.waitFor(() => expect(pagesAPI.getPages).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(document.querySelector('.export-toggle').disabled).toBe(false));

    document.querySelector('.export-toggle').click();
    await vi.waitFor(() => expect(document.querySelector('.export-menu-item')).not.toBeNull());
    document.querySelector('.export-menu-item').click();
    await vi.waitFor(() => expect(onerror).toHaveBeenCalledWith('export unavailable'));
  });
});
