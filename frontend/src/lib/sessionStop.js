export function sessionStopLabel(session) {
  if (!session || session.Status !== 'stopped' || !session.StopReason) return '';
  switch (session.StopReason) {
    case 'shutdown':
      return 'interrupted by restart';
    case 'manual':
      return 'manual stop';
    case 'orphaned':
      return 'recovered after restart';
    default:
      return session.StopReason;
  }
}

export function sessionStopTitle(session) {
  const label = sessionStopLabel(session);
  if (!label) return '';
  const parts = [label];
  if (session.StopMessage) parts.push(session.StopMessage);
  if (session.StopAt) parts.push(`Recorded at ${new Date(session.StopAt).toLocaleString()}`);
  return parts.join(' - ');
}
