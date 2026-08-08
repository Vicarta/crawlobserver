import { describe, expect, it } from 'vitest';
import { qualityRepairOutcome } from './quality-repair.js';

describe('qualityRepairOutcome', () => {
  it.each([
    [{ changed: true }, 'quality.repairPublished', 'success'],
    [{ changed: false }, 'quality.repairUnchanged', 'success'],
    [
      { changed: false, promotion_changed: true, promotion: { status: 'applied' } },
      'quality.promotionRepaired',
      'success',
    ],
    [
      { changed: true, promotion_changed: true, promotion: { status: 'failed' } },
      'quality.promotionFailed',
      'error',
    ],
    [{ promotion: { status: 'rejected' } }, 'quality.promotionRejected', 'error'],
    [{ promotion: { status: 'conflict' } }, 'quality.promotionConflict', 'conflict'],
    [{ promotion: { status: 'superseded' } }, 'quality.promotionSuperseded', 'conflict'],
  ])('maps %o to a typed outcome', (response, messageKey, state) => {
    expect(qualityRepairOutcome(response)).toEqual({ messageKey, state });
  });
});
