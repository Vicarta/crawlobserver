const terminalPromotionStates = new Set(['failed', 'rejected', 'conflict', 'superseded']);

export function qualityRepairOutcome(response = {}) {
  const promotionStatus = response.promotion?.status || '';
  if (terminalPromotionStates.has(promotionStatus)) {
    return {
      state: ['conflict', 'superseded'].includes(promotionStatus) ? 'conflict' : 'error',
      messageKey: `quality.promotion${promotionStatus[0].toUpperCase()}${promotionStatus.slice(1)}`,
    };
  }
  if (response.promotion_changed) {
    return { state: 'success', messageKey: 'quality.promotionRepaired' };
  }
  if (response.changed) {
    return { state: 'success', messageKey: 'quality.repairPublished' };
  }
  return { state: 'success', messageKey: 'quality.repairUnchanged' };
}

export async function loadAuthoritativeQualityRepairState(
  sessionId,
  { loadQuality, loadHistory, loadEvidence },
) {
  const [result, history, evidence] = await Promise.all([
    loadQuality(sessionId),
    loadHistory(sessionId),
    loadEvidence(sessionId),
  ]);
  return { result, history: history || [], evidence: evidence || null };
}

export async function runQualityReevaluationFlow(
  sessionId,
  request,
  {
    reevaluate,
    loadQuality,
    loadHistory,
    loadEvidence,
    refreshSnapshot,
    applyAuthoritativeState,
    updateBadge,
    reportError,
  },
) {
  let response;
  try {
    response = await reevaluate(sessionId, request);
  } catch (error) {
    if (error.status !== 409) {
      reportError?.(error.message);
      return { kind: 'error', error };
    }

    try {
      const state = await loadAuthoritativeQualityRepairState(sessionId, {
        loadQuality,
        loadHistory,
        loadEvidence,
      });
      applyAuthoritativeState(state);
      updateBadge(state.result);
      return { kind: 'conflict', error, state };
    } catch (refreshError) {
      reportError?.(refreshError.message);
      return { kind: 'conflict', error, refreshError };
    }
  }

  try {
    const state = await loadAuthoritativeQualityRepairState(sessionId, {
      loadQuality,
      loadHistory,
      loadEvidence,
    });
    await refreshSnapshot();
    applyAuthoritativeState(state);
    updateBadge(state.result);
    return {
      kind: 'success',
      response,
      state,
      outcome: qualityRepairOutcome(response),
    };
  } catch (error) {
    reportError?.(error.message);
    return { kind: 'error', error };
  }
}
