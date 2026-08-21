function startTimestamp(record) {
  const value = record?.startedAt || record?.timestamp;
  if (!value) return null;
  const parsed = typeof value === 'number' ? value : Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function endTimestamp(record) {
  const value = record?.completedAt || (record?.startedAt && record?.timestamp ? record.timestamp : null);
  if (!value) return null;
  const parsed = typeof value === 'number' ? value : Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export function groupTrajectoryRecords(records = [], collapsed = new Set()) {
  const groups = [];
  const byGroup = new Map();
  for (const record of records) {
    const key = `${record.sessionId || 'session'}:${record.runId || 'no-run'}:${record.attempt || 0}:${record.turn || 0}`;
    let group = byGroup.get(key);
    if (!group) {
      group = { id: key, runId: record.runId || '', attempt: record.attempt || 0, records: [], collapsed: collapsed.has(key) };
      byGroup.set(key, group);
      groups.push(group);
    }
    group.records.push(record);
  }
  return groups;
}

export function flattenTrajectoryGroups(groups = []) {
  return groups.flatMap((group) => {
    if (!group.collapsed) return group.records.map((record) => ({ ...record, groupID: group.id }));
    const toolCount = group.records.filter((record) => record.kind === 'tool').length;
    const groupSummary = String(group.records.length) + ' steps' + (toolCount ? ', ' + String(toolCount) + ' tools' : '');
    return [{ ...group.records[0], groupID: group.id, groupSummary, isGroupSummary: true }];
  });
}

export function timelineSpans(records = [], range = null) {
  const timed = records.map((record) => ({ record, start: startTimestamp(record), end: endTimestamp(record) })).filter((item) => item.start !== null);
  const unknown = records.filter((record) => startTimestamp(record) === null);
  if (!timed.length) {
    return {
      min: range?.min ?? 0,
      max: range?.max ?? 1,
      spans: unknown.map((record, index) => ({
        id: record.id,
        left: unknown.length > 1 ? (index / unknown.length) * 100 : 0,
        width: 0.8,
        unknownEnd: true,
        unknownTime: true
      }))
    };
  }
  const min = range?.min ?? Math.min(...timed.map((item) => item.start));
  const max = range?.max ?? Math.max(...timed.map((item) => item.end || item.start));
  const width = Math.max(1, max - min);
  return {
    min,
    max,
    spans: timed.map(({ record, start, end }) => ({
      id: record.id,
      left: Math.max(0, ((start - min) / width) * 100),
      width: Math.max(0.45, (((end || start) - start) / width) * 100),
      unknownEnd: !end
    })).concat(unknown.map((record, index) => ({
      id: record.id,
      left: Math.min(99, 96 + (index * 0.8)),
      width: 0.8,
      unknownEnd: true,
      unknownTime: true
    })))
  };
}

export function visibleWindow(items = [], scrollTop = 0, viewportHeight = 560, rowHeight = 58, overscan = 8) {
  const total = items.length;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(total, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  return { start, end, items: items.slice(start, end), before: start * rowHeight, after: Math.max(0, (total - end) * rowHeight), total };
}
