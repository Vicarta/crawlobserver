import { downloadCSV } from './utils.js';

/**
 * Build the explicit CSV payload for a selected row set.
 * The config controls the allowlisted columns; whole API objects are never
 * serialized implicitly.
 */
export function buildSelectedCSV(config, rows = []) {
  if (!config?.headers || !(config.keys || config.customKeys)) {
    throw new Error('Selected CSV config requires headers and keys');
  }

  const keys = config.customKeys || config.keys;
  const projectedRows = config.transform ? rows.map(config.transform) : [...rows];

  return {
    headers: [...config.headers],
    keys: [...keys],
    rows: projectedRows,
  };
}

export function exportSelectedCSV(filename, config, rows = []) {
  const payload = buildSelectedCSV(config, rows);
  downloadCSV(filename, payload.headers, payload.keys, payload.rows);
  return payload.rows.length;
}

/**
 * Stable identity for an interlinking row across pagination and sorting.
 */
export function opportunitySelectionKey(opportunity) {
  return JSON.stringify([
    opportunity?.source_url || '',
    opportunity?.target_url || '',
    opportunity?.category || '',
  ]);
}
