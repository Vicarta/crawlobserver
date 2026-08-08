import { describe, expect, it } from 'vitest';
import { buildSelectedCSV, opportunitySelectionKey } from './selectedCsv.js';

describe('buildSelectedCSV', () => {
  it('keeps the configured column order and selected row order', () => {
    const payload = buildSelectedCSV(
      {
        headers: ['URL', 'Status'],
        keys: ['url', 'status'],
        transform: (row) => ({ url: row.URL, status: row.StatusCode }),
      },
      [
        { URL: 'https://example.com/a', StatusCode: 200 },
        { URL: 'https://example.com/b', StatusCode: 404 },
      ],
    );

    expect(payload.headers).toEqual(['URL', 'Status']);
    expect(payload.keys).toEqual(['url', 'status']);
    expect(payload.rows).toEqual([
      { url: 'https://example.com/a', status: 200 },
      { url: 'https://example.com/b', status: 404 },
    ]);
  });

  it('does not add hidden API fields to the CSV allowlist', () => {
    const payload = buildSelectedCSV({ headers: ['URL'], keys: ['URL'] }, [
      { URL: 'https://example.com/', Config: 'secret', InternalID: 'hidden' },
    ]);

    expect(payload.keys).toEqual(['URL']);
    expect(payload.keys).not.toContain('Config');
    expect(payload.keys).not.toContain('InternalID');
  });

  it('supports values that require escaping by the shared CSV serializer', () => {
    const payload = buildSelectedCSV({ headers: ['Title'], keys: ['title'] }, [
      { title: 'Comma, quote " and\nnewline' },
    ]);

    expect(payload.rows[0].title).toBe('Comma, quote " and\nnewline');
  });

  it('rejects an incomplete projection config', () => {
    expect(() => buildSelectedCSV({ headers: ['URL'] }, [])).toThrow(
      'Selected CSV config requires headers and keys',
    );
  });
});

describe('opportunitySelectionKey', () => {
  it('is stable for the same source, target, and category', () => {
    const row = {
      source_url: 'https://example.com/a',
      target_url: 'https://example.com/b',
      category: 'opportunity',
    };

    expect(opportunitySelectionKey(row)).toBe(opportunitySelectionKey({ ...row }));
  });

  it('keeps otherwise identical categories distinct', () => {
    const row = {
      source_url: 'https://example.com/a',
      target_url: 'https://example.com/b',
      category: 'opportunity',
    };

    expect(opportunitySelectionKey(row)).not.toBe(
      opportunitySelectionKey({ ...row, category: 'cannibalization' }),
    );
  });

  it('does not collide when URLs contain delimiter-like text', () => {
    expect(
      opportunitySelectionKey({
        source_url: 'https://example.com/a|b',
        target_url: 'https://example.com/c',
        category: 'opportunity',
      }),
    ).not.toBe(
      opportunitySelectionKey({
        source_url: 'https://example.com/a',
        target_url: 'b|https://example.com/c',
        category: 'opportunity',
      }),
    );
  });
});
