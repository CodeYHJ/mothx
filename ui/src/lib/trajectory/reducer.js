import { mergeTrajectoryRecords, normalizeTrajectoryRecords } from './records.js';

export function emptyTrajectoryState() {
  return {
    recordsByID: {},
    orderedIDs: [],
    highWater: { entrySeq: 0, runSeq: 0, capabilitySeq: 0, decisionSeq: 0 },
    errors: {},
    loading: false,
    hasMore: false
  };
}

function maxSeq(items = []) {
  return items.reduce((max, item) => Math.max(max, Number(item?.seq || 0)), 0);
}

function recordsFromState(state) {
  return (state?.orderedIDs || []).map((id) => state.recordsByID?.[id]).filter(Boolean);
}

export function reduceTrajectoryState(state = emptyTrajectoryState(), input = {}) {
  const incoming = normalizeTrajectoryRecords(input);
  const merged = mergeTrajectoryRecords([...recordsFromState(state), ...incoming]);
  const recordsByID = Object.fromEntries(merged.map((record) => [record.id, record]));
  const highWater = {
    entrySeq: Math.max(Number(state.highWater?.entrySeq || 0), Number(input.highWater?.entrySeq || 0), maxSeq(input.messages)),
    runSeq: Math.max(Number(state.highWater?.runSeq || 0), Number(input.highWater?.runSeq || 0), maxSeq(input.runEvents)),
    capabilitySeq: Math.max(Number(state.highWater?.capabilitySeq || 0), Number(input.highWater?.capabilitySeq || 0), maxSeq(input.capabilityEvents)),
    decisionSeq: Math.max(Number(state.highWater?.decisionSeq || 0), Number(input.highWater?.decisionSeq || 0), maxSeq(input.decisionEvents))
  };
  return {
    ...state,
    recordsByID,
    orderedIDs: merged.map((record) => record.id),
    highWater,
    loading: input.loading === undefined ? state.loading : Boolean(input.loading),
    hasMore: input.hasMore === undefined ? state.hasMore : Boolean(input.hasMore),
    errors: input.errors ? { ...state.errors, ...input.errors } : state.errors
  };
}

export function applyTrajectoryEvent(state = emptyTrajectoryState(), source, event, sessionId = '') {
  const input = { sessionId };
  if (source === 'transcript') input.messages = [event];
  else if (source === 'tool') input.toolEvents = [event];
  else if (source === 'run') input.runEvents = [event];
  else if (source === 'capability') input.capabilityEvents = [event];
  else if (source === 'decision') input.decisionEvents = [event];
  else return state;
  return reduceTrajectoryState(state, input);
}

export function trajectoryRecords(state = emptyTrajectoryState()) {
  return (state.orderedIDs || []).map((id) => state.recordsByID?.[id]).filter(Boolean);
}
