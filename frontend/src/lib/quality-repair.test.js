import { describe, expect, it, vi } from 'vitest';
import {
  loadAuthoritativeQualityRepairState,
  qualityRepairOutcome,
  runQualityReevaluationFlow,
} from './quality-repair.js';

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

describe('loadAuthoritativeQualityRepairState', () => {
  it('reloads complete quality, history, and evidence instead of rendering a partial POST result', async () => {
    const authoritative = {
      evaluation_revision: 'evaluation-v3',
      findings: [{ finding_type: 'crawl_config_changed' }],
    };
    const loadQuality = vi.fn().mockResolvedValue(authoritative);
    const loadHistory = vi.fn().mockResolvedValue([authoritative]);
    const loadEvidence = vi.fn().mockResolvedValue({ attempt_id: 'evidence-1' });

    const state = await loadAuthoritativeQualityRepairState('session-1', {
      loadQuality,
      loadHistory,
      loadEvidence,
    });

    expect(state).toEqual({
      result: authoritative,
      history: [authoritative],
      evidence: { attempt_id: 'evidence-1' },
    });
    expect(state.result.findings).toHaveLength(1);
    expect(loadQuality).toHaveBeenCalledWith('session-1');
    expect(loadHistory).toHaveBeenCalledWith('session-1');
    expect(loadEvidence).toHaveBeenCalledWith('session-1');
  });
});

describe('runQualityReevaluationFlow', () => {
  function setup({ response = { changed: true }, postError } = {}) {
    const authoritative = {
      evaluation_revision: 'evaluation-v3',
      findings: [{ finding_type: 'crawl_config_changed' }],
    };
    const dependencies = {
      reevaluate: postError
        ? vi.fn().mockRejectedValue(postError)
        : vi.fn().mockResolvedValue(response),
      loadQuality: vi.fn().mockResolvedValue(authoritative),
      loadHistory: vi.fn().mockResolvedValue([authoritative]),
      loadEvidence: vi.fn().mockResolvedValue({ attempt_id: 'evidence-1' }),
      refreshSnapshot: vi.fn().mockResolvedValue(undefined),
      applyAuthoritativeState: vi.fn(),
      updateBadge: vi.fn(),
      reportError: vi.fn(),
    };
    return { authoritative, dependencies };
  }

  it('uses authoritative findings and badge after a successful partial POST response', async () => {
    const { authoritative, dependencies } = setup({
      response: { changed: true, result: { evaluation_revision: 'evaluation-v3' } },
    });

    const result = await runQualityReevaluationFlow(
      'session-1',
      { confirm: true, reason: 'repair' },
      dependencies,
    );

    expect(result.kind).toBe('success');
    expect(result.state.result.findings).toEqual([{ finding_type: 'crawl_config_changed' }]);
    expect(dependencies.applyAuthoritativeState).toHaveBeenCalledWith(result.state);
    expect(dependencies.updateBadge).toHaveBeenCalledWith(authoritative);
    expect(dependencies.refreshSnapshot).toHaveBeenCalledOnce();
  });

  it('uses the same authoritative GET and snapshot path for an idempotent response', async () => {
    const { authoritative, dependencies } = setup({ response: { changed: false } });

    const result = await runQualityReevaluationFlow('session-1', {}, dependencies);

    expect(result).toMatchObject({
      kind: 'success',
      outcome: { state: 'success', messageKey: 'quality.repairUnchanged' },
    });
    expect(dependencies.loadQuality).toHaveBeenCalledWith('session-1');
    expect(dependencies.loadHistory).toHaveBeenCalledWith('session-1');
    expect(dependencies.loadEvidence).toHaveBeenCalledWith('session-1');
    expect(dependencies.updateBadge).toHaveBeenCalledWith(authoritative);
    expect(dependencies.refreshSnapshot).toHaveBeenCalledOnce();
  });

  it('refreshes authoritative quality on a 409 without running the success snapshot path', async () => {
    const conflict = Object.assign(new Error('revision conflict'), { status: 409 });
    const { authoritative, dependencies } = setup({ postError: conflict });

    const result = await runQualityReevaluationFlow('session-1', {}, dependencies);

    expect(result).toMatchObject({ kind: 'conflict', error: conflict });
    expect(dependencies.applyAuthoritativeState).toHaveBeenCalledWith(result.state);
    expect(dependencies.updateBadge).toHaveBeenCalledWith(authoritative);
    expect(dependencies.refreshSnapshot).not.toHaveBeenCalled();
    expect(dependencies.reportError).not.toHaveBeenCalled();
  });

  it('preserves the prior complete state and reports a non-409 error', async () => {
    const priorState = {
      result: { findings: [{ finding_type: 'crawl_config_changed' }] },
    };
    let renderedState = priorState;
    const error = Object.assign(new Error('service unavailable'), { status: 503 });
    const { dependencies } = setup({ postError: error });
    dependencies.applyAuthoritativeState.mockImplementation((state) => {
      renderedState = state;
    });

    const result = await runQualityReevaluationFlow('session-1', {}, dependencies);

    expect(result).toEqual({ kind: 'error', error });
    expect(renderedState).toBe(priorState);
    expect(dependencies.applyAuthoritativeState).not.toHaveBeenCalled();
    expect(dependencies.updateBadge).not.toHaveBeenCalled();
    expect(dependencies.refreshSnapshot).not.toHaveBeenCalled();
    expect(dependencies.reportError).toHaveBeenCalledWith('service unavailable');
  });
});
