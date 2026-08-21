function searchable(record) {
  return [record.summary, record.preview, record.kind, record.status, record.toolCallId, record.runId, JSON.stringify(record.input || ''), JSON.stringify(record.output || '')]
    .filter(Boolean).join(' ').toLowerCase();
}

export function createTrajectorySearch(records = []) {
  const index = new Map(records.map((record) => [record.id, searchable(record)]));
  return {
    query(query = '', filters = {}) {
      const needle = String(query || '').trim().toLowerCase();
      return records.filter((record) => {
        if (filters.kind && filters.kind !== 'all' && record.kind !== filters.kind) return false;
        if (filters.status && filters.status !== 'all' && record.status !== filters.status) return false;
        return !needle || index.get(record.id)?.includes(needle);
      });
    }
  };
}
