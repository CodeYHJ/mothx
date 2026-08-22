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

export function visibleWindow(items = [], scrollTop = 0, viewportHeight = 560, rowHeight = 58, overscan = 8) {
  const total = items.length;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const end = Math.min(total, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan);
  return { start, end, items: items.slice(start, end), before: start * rowHeight, after: Math.max(0, (total - end) * rowHeight), total };
}
