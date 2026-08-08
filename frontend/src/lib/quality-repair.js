const terminalPromotionStates = new Set(['failed', 'rejected', 'conflict']);

export function qualityRepairOutcome(response = {}) {
  const promotionStatus = response.promotion?.status || '';
  if (terminalPromotionStates.has(promotionStatus)) {
    return {
      state: promotionStatus === 'conflict' ? 'conflict' : 'error',
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
