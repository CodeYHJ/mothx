// Pure per-session transcript view reducers.
//
// The chat view renders one session at a time, but run events arrive for every
// subscribed session. These reducers apply stream events to a plain "view"
// object without any DOM/Svelte side effects, so background sessions update
// their own state directly instead of swapping the visible transcript.
//
// A view looks like:
// {
//   messages, toolEvents, runEvents, capabilityEvents,
//   runtime, cursor: { entrySeq, runSeq, capabilitySeq },
//   streamCompleted, subAgents, subAgentTranscripts
// }
//
// Reducers take (view, ...) and return { view, effects }. `effects` carries
// intent for UI-only side effects (scrolling, sub-agent refresh) that the
// caller applies only when the session is currently visible.

import { approvalRequestOwnership, applyApprovalRequestToRuntime } from './approval.js';

export function emptySessionView() {
  return {
    messages: [],
    toolEvents: [],
    runEvents: [],
    capabilityEvents: [],
    runtime: null,
    cursor: { entrySeq: 0, runSeq: 0, capabilitySeq: 0 },
    streamCompleted: false,
    subAgents: [],
    subAgentTranscripts: {}
  };
}

export function viewFromSessionState(state = {}) {
  return {
    messages: state.messages || [],
    toolEvents: state.toolEvents || [],
    runEvents: state.runEvents || [],
    capabilityEvents: state.capabilityEvents || [],
    runtime: state.runtime || null,
    cursor: state.cursor || { entrySeq: 0, runSeq: 0, capabilitySeq: 0 },
    streamCompleted: Boolean(state.streamCompleted),
    subAgents: state.subAgents || [],
    subAgentTranscripts: state.subAgentTranscripts || {}
  };
}

export function sessionStateWithView(state, view) {
  return {
    ...state,
    messages: view.messages,
    toolEvents: view.toolEvents,
    runEvents: view.runEvents,
    capabilityEvents: view.capabilityEvents,
    runtime: view.runtime,
    cursor: view.cursor,
    streamCompleted: view.streamCompleted,
    subAgents: view.subAgents,
    subAgentTranscripts: view.subAgentTranscripts
  };
}

// --- generic helpers ---

export function isPlainObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value);
}

export function normalizeJSONValue(value) {
  if (typeof value !== 'string') return value;
  const trimmed = value.trim();
  if (!trimmed) return value;
  if (!['{', '[', '"'].includes(trimmed[0]) && !/^(true|false|null|-?\d)/.test(trimmed)) return value;
  try {
    return JSON.parse(trimmed);
  } catch {
    return value;
  }
}

export function stringFrom(value) {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  return String(value);
}

export function maxSeq(items = []) {
  return items.reduce((max, item) => {
    const seq = Number(item?.seq || 0);
    return seq > max ? seq : max;
  }, 0);
}

function bumpCursorFromSeq(view, key, seq) {
  const value = Number(seq || 0);
  if (value > (view.cursor?.[key] || 0)) {
    return { ...view, cursor: { ...view.cursor, [key]: value } };
  }
  return view;
}

function countTextLines(text = '') {
  if (!text) return 0;
  return String(text).split('\n').length;
}

function compactText(text = '', limit = 120) {
  const normalized = String(text || '').replace(/\s+/g, ' ').trim();
  if (normalized.length <= limit) return normalized;
  return `${normalized.slice(0, Math.max(0, limit - 1))}...`;
}

function browserTarget(value = {}) {
  return value.url
    || value.selector
    || value.outputPath
    || value.text
    || value.value
    || value.key
    || value.attr
    || value.targetId
    || value.session
    || '';
}

export function textFromContents(contents = []) {
  return contents
    .filter((block) => block.type === 'text' && block.text)
    .map((block) => block.text)
    .join('\n');
}

// --- tool result parsers ---

export function parseReadResult(content = '') {
  if (!content) return [];
  const lines = content.split('\n').filter((line) => line.length > 0);
  const parsed = [];
  for (const line of lines) {
    const match = line.match(/^(\d+)\t(.*)$/);
    if (!match) return [];
    parsed.push({ number: match[1], text: match[2] });
  }
  return parsed;
}

export function parseLsResult(content = '') {
  if (!content || content === '(empty directory)') return [];
  const entries = [];
  for (const line of content.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const dir = trimmed.match(/^📁\s+(.+)\/$/);
    if (dir) {
      entries.push({ type: 'dir', name: dir[1], size: '' });
      continue;
    }
    const file = trimmed.match(/^📄\s+(.+)\s+\(([^)]+)\)$/);
    if (file) {
      entries.push({ type: 'file', name: file[1], size: file[2] });
      continue;
    }
    return [];
  }
  return entries;
}

export function parseGrepResult(content = '') {
  const result = { matches: [], note: '' };
  if (!content || content === '(no matches found)') return result;
  for (const line of content.split('\n')) {
    if (!line) continue;
    if (line.startsWith('... (truncated')) {
      result.note = line;
      continue;
    }
    const match = line.match(/^(.+):(\d+):(.*)$/);
    if (!match) return { matches: [], note: '' };
    result.matches.push({ path: match[1], line: match[2], text: match[3] });
  }
  return result;
}

function parseTaggedSections(content = '') {
  const sections = { __prefix: [] };
  let current = '__prefix';
  for (const line of content.split('\n')) {
    const match = line.match(/^\[([a-z_]+)\]$/);
    if (match) {
      current = match[1];
      if (!sections[current]) sections[current] = [];
      continue;
    }
    sections[current].push(line);
  }
  const out = {};
  for (const [key, lines] of Object.entries(sections)) {
    out[key] = lines.join('\n').trim();
  }
  return out;
}

export function parseBashResult(content = '') {
  if (!content) return null;
  const sections = parseTaggedSections(content);
  if (!sections.runtime && !sections.command && !sections.stdout && !sections.stderr && !sections.exit_code) {
    return null;
  }
  let command = sections.command || '';
  let note = '';
  const noteIndex = command.indexOf("\nUse 'jobs' tool");
  if (noteIndex >= 0) {
    note = command.slice(noteIndex + 1).trim();
    command = command.slice(0, noteIndex).trimEnd();
  }
  return {
    runtime: sections.runtime || '',
    command,
    cwd: sections.cwd || '',
    stdout: sections.stdout || '',
    stderr: sections.stderr || '',
    exitCode: sections.exit_code || '',
    note,
    prefix: sections.__prefix || ''
  };
}

export function parseBrowserResult(content = '') {
  const parsed = normalizeJSONValue(content);
  if (isPlainObject(parsed)) {
    return {
      status: stringFrom(parsed.status || parsed.message || parsed.result || parsed.action || 'browser result'),
      title: stringFrom(parsed.title || parsed.pageTitle || ''),
      url: stringFrom(parsed.url || parsed.href || parsed.currentURL || parsed.currentUrl || ''),
      content: JSON.stringify(parsed, null, 2)
    };
  }
  const text = String(content || '').trim();
  if (!text) return null;
  const lines = text.split('\n').map((line) => line.trim()).filter(Boolean);
  const first = lines[0] || '';
  const titleLine = lines.find((line) => line.toLowerCase().startsWith('title:'));
  const urlLine = lines.find((line) => line.toLowerCase().startsWith('url:'));
  return {
    status: first,
    title: titleLine ? titleLine.replace(/^title:\s*/i, '') : '',
    url: urlLine ? urlLine.replace(/^url:\s*/i, '') : '',
    content: text
  };
}

export function parseSubAgentResult(content = '') {
  if (isPlainObject(content)) return content;
  const text = String(content || '').trim();
  if (!text) return null;
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed === 'object') return parsed;
  } catch {
    // fall through to plain text
  }
  return { result: text };
}

export function parseWorkflowLintResult(content = '') {
  const parsed = normalizeJSONValue(content);
  if (!isPlainObject(parsed) || !Object.prototype.hasOwnProperty.call(parsed, 'valid')) return null;
  return {
    valid: parsed.valid === true,
    status: stringFrom(parsed.status || (parsed.valid ? 'done' : 'error')),
    error: stringFrom(parsed.error || ''),
    tasks: Array.isArray(parsed.tasks) ? parsed.tasks.map(stringFrom).filter(Boolean) : [],
    results: Array.isArray(parsed.results) ? parsed.results.map(stringFrom).filter(Boolean) : [],
    raw: JSON.stringify(parsed, null, 2)
  };
}

export function isSubAgentTool(toolName) {
  return ['delegate_subagent', 'subagent_spawn', 'subagent_status', 'subagent_send', 'subagent_destroy'].includes(toolName);
}

export function toolResultKind(toolName, content) {
  if (toolName === 'read' && parseReadResult(content).length > 0) return 'read';
  if (toolName === 'ls' && (parseLsResult(content).length > 0 || content === '(empty directory)')) return 'ls';
  if (toolName === 'grep' && (parseGrepResult(content).matches.length > 0 || content === '(no matches found)')) return 'grep';
  if (toolName === 'bash' && parseBashResult(content)) return 'bash';
  if (toolName === 'browser') return 'browser';
  if (toolName === 'skill_ref') return 'skill-ref';
  if (toolName === 'workflow_lint' && parseWorkflowLintResult(content)) return 'workflow-lint';
  if (isSubAgentTool(toolName)) return 'subagent';
  return 'text';
}

// --- tool call view / message normalization ---

export function buildToolCallView(toolName, args, invalidArguments = '', tr = (k) => k) {
  const name = toolName || 'tool';
  const raw = normalizeJSONValue(args);
  const value = isPlainObject(raw) ? raw : {};
  if (name === 'read') {
    const details = [];
    if (value.offset) details.push(tr('chat.tool.read.offset', { offset: value.offset }));
    if (value.limit) details.push(tr('chat.tool.read.limit', { limit: value.limit }));
    if (value.imageMode) details.push(tr('chat.tool.read.imageMode', { mode: value.imageMode }));
    if (value.maxLongEdge) details.push(tr('chat.tool.read.maxLongEdge', { value: value.maxLongEdge }));
    if (value.crop) details.push(tr('chat.tool.read.crop', { value: `${value.crop.width || 0}x${value.crop.height || 0}+${value.crop.x || 0}+${value.crop.y || 0}` }));
    return {
      kind: 'read',
      label: tr('chat.tool.read.label'),
      target: value.path || tr('chat.tool.read.missing'),
      details,
      raw,
      invalidArguments
    };
  }
  if (name === 'ls') {
    return {
      kind: 'ls',
      label: tr('chat.tool.ls.label'),
      target: value.path || '.',
      details: [],
      raw,
      invalidArguments
    };
  }
  if (name === 'grep') {
    const details = [];
    if (value.path) details.push(value.path);
    if (value.include) details.push(`include ${value.include}`);
    if (value.maxResults) details.push(tr('chat.tool.grep.maxResults', { count: value.maxResults }));
    return {
      kind: 'grep',
      label: tr('chat.tool.grep.label'),
      target: value.pattern || tr('chat.tool.grep.missing'),
      details,
      raw,
      invalidArguments
    };
  }
  if (name === 'find') {
    const details = [];
    if (value.path) details.push(tr('chat.tool.find.path', { path: value.path }));
    if (value.maxDepth !== undefined && value.maxDepth !== null) {
      details.push(tr('chat.tool.find.maxDepth', { depth: value.maxDepth }));
    }
    if (value.maxResults !== undefined && value.maxResults !== null) {
      details.push(tr('chat.tool.find.maxResults', { count: value.maxResults }));
    }
    return {
      kind: 'find',
      label: tr('chat.tool.find.label'),
      target: value.pattern || tr('chat.tool.find.missing'),
      details,
      pattern: value.pattern || '',
      path: value.path || '.',
      maxDepth: value.maxDepth ?? '',
      maxResults: value.maxResults ?? '',
      raw,
      invalidArguments
    };
  }
  if (name === 'bash') {
    const details = [];
    if (value.async) details.push(tr('chat.tool.bash.async'));
    if (value.timeout !== undefined && value.timeout !== null) {
      details.push(Number(value.timeout) === 0 ? tr('chat.tool.bash.noTimeout') : tr('chat.tool.bash.timeout', { seconds: value.timeout }));
    }
    return {
      kind: 'bash',
      label: tr('chat.tool.bash.label'),
      target: value.command || tr('chat.tool.bash.missing'),
      details,
      raw,
      invalidArguments
    };
  }
  if (name === 'edit') {
    const edits = Array.isArray(value.edits)
      ? value.edits
        .filter(isPlainObject)
        .map((item, index) => {
          const oldText = String(item.oldText ?? '');
          const newText = String(item.newText ?? '');
          return {
            index: index + 1,
            oldText,
            newText,
            oldLines: countTextLines(oldText),
            newLines: countTextLines(newText)
          };
        })
      : [];
    return {
      kind: 'edit',
      label: tr('chat.tool.edit.label'),
      target: value.path || tr('chat.tool.edit.missing'),
      details: [edits.length === 1 ? tr('chat.tool.edit.oneEdit') : tr('chat.tool.edit.manyEdits', { count: edits.length })],
      edits,
      raw,
      invalidArguments
    };
  }
  if (name === 'write') {
    const content = typeof value.content === 'string' ? value.content : '';
    const lines = countTextLines(content);
    const chars = content.length;
    return {
      kind: 'write',
      label: tr('chat.tool.write.label'),
      target: value.path || tr('chat.tool.write.missing'),
      details: [
        tr('chat.tool.write.lines', { count: lines }),
        tr('chat.tool.write.chars', { count: chars })
      ],
      content,
      lines,
      chars,
      raw,
      invalidArguments
    };
  }
  if (name === 'insert') {
    const content = typeof value.content === 'string' ? value.content : '';
    const lines = countTextLines(content);
    const chars = content.length;
    const pos = isPlainObject(value.position) ? value.position : {};
    const posType = String(pos.type || '').trim();
    const posLine = Number.isInteger(pos.line) ? pos.line : Number(pos.line) || 0;
    const details = [];
    if (posType === 'head') details.push(`${tr('chat.tool.insert.position')}: ${tr('chat.tool.insert.positionHead')}`);
    else if (posType === 'tail') details.push(`${tr('chat.tool.insert.position')}: ${tr('chat.tool.insert.positionTail')}`);
    else if (posType === 'before_line' && posLine > 0) details.push(`${tr('chat.tool.insert.position')}: ${tr('chat.tool.insert.positionBeforeLine', { line: posLine })}`);
    else if (posType === 'after_line' && posLine > 0) details.push(`${tr('chat.tool.insert.position')}: ${tr('chat.tool.insert.positionAfterLine', { line: posLine })}`);
    details.push(tr('chat.tool.insert.lines', { count: lines }));
    details.push(tr('chat.tool.insert.chars', { count: chars }));
    if (value.dry_run) details.push(tr('chat.tool.insert.dryRun'));
    if (isPlainObject(value.dedupe) && value.dedupe.enabled) details.push(tr('chat.tool.insert.dedupe', { mode: value.dedupe.mode || 'exact' }));
    if (value.create_if_missing) details.push(tr('chat.tool.insert.createIfMissing'));
    return {
      kind: 'insert',
      label: tr('chat.tool.insert.label'),
      target: value.path || tr('chat.tool.insert.missing'),
      details,
      content,
      lines,
      chars,
      positionType: posType,
      positionLine: posLine,
      raw,
      invalidArguments
    };
  }
  if (name === 'browser') {
    const details = [];
    const action = String(value.action || '').trim();
    if (value.selector) details.push(tr('chat.tool.browser.selector', { selector: value.selector }));
    if (value.outputPath) details.push(tr('chat.tool.browser.output', { path: value.outputPath }));
    if (value.fullPage) details.push(tr('chat.tool.browser.fullPage'));
    if (value.interactive) details.push(tr('chat.tool.browser.interactive'));
    if (value.width || value.height) details.push(tr('chat.tool.browser.viewport', { width: value.width || '?', height: value.height || '?' }));
    if (value.viewportWidth || value.viewportHeight) details.push(tr('chat.tool.browser.viewport', { width: value.viewportWidth || '?', height: value.viewportHeight || '?' }));
    if (value.format) details.push(String(value.format));
    return {
      kind: 'browser',
      label: tr('chat.tool.browser.label'),
      target: browserTarget(value) || tr('chat.tool.browser.missing'),
      details,
      action,
      selector: value.selector || '',
      url: value.url || '',
      value: value.value ?? value.text ?? value.key ?? '',
      expression: value.expression || '',
      raw,
      invalidArguments
    };
  }
  if (name === 'skill_ref') {
    return {
      kind: 'skill-ref',
      label: tr('chat.tool.skillRef.label'),
      target: value.skill && value.ref ? `${value.skill}/${value.ref}` : tr('chat.tool.skillRef.missing'),
      details: [
        value.skill ? tr('chat.tool.skillRef.skill', { skill: value.skill }) : '',
        value.ref ? tr('chat.tool.skillRef.ref', { ref: value.ref }) : ''
      ].filter(Boolean),
      skill: value.skill || '',
      ref: value.ref || '',
      raw,
      invalidArguments
    };
  }
  if (name === 'workflow_lint') {
    const source = typeof value.source === 'string' ? value.source : '';
    const firstLine = source.split('\n').map((line) => line.trim()).find(Boolean) || '';
    const lines = countTextLines(source);
    const chars = source.length;
    return {
      kind: 'workflow-lint',
      label: tr('chat.tool.workflowLint.label'),
      target: firstLine ? compactText(firstLine, 120) : tr('chat.tool.workflowLint.missing'),
      details: [
        tr('chat.tool.workflowLint.lines', { count: lines }),
        tr('chat.tool.workflowLint.chars', { count: chars })
      ],
      source,
      lines,
      chars,
      raw,
      invalidArguments
    };
  }
  if (name === 'delegate_subagent' || name === 'subagent_spawn') {
    const details = [];
    if (value.mode) details.push(tr('chat.tool.subagent.mode', { mode: value.mode }));
    if (value.work_dir) details.push(tr('chat.tool.subagent.workDir', { path: value.work_dir }));
    if (Array.isArray(value.tools) && value.tools.length > 0) details.push(tr('chat.tool.subagent.tools', { tools: value.tools.join(', ') }));
    if (value.max_iterations) details.push(tr('chat.tool.subagent.maxIterations', { count: value.max_iterations }));
    return {
      kind: 'subagent-task',
      label: name === 'delegate_subagent' ? tr('chat.tool.subagent.delegate') : tr('chat.tool.subagent.spawn'),
      target: compactText(value.task || tr('chat.tool.subagent.taskMissing'), 140),
      details,
      task: value.task || '',
      raw,
      invalidArguments
    };
  }
  if (name === 'subagent_status' || name === 'subagent_destroy' || name === 'subagent_send') {
    const details = [];
    if (name === 'subagent_send' && value.message) details.push(compactText(value.message, 120));
    return {
      kind: 'subagent-handle',
      label: name === 'subagent_status'
        ? tr('chat.tool.subagent.status')
        : name === 'subagent_destroy'
          ? tr('chat.tool.subagent.destroy')
          : tr('chat.tool.subagent.send'),
      target: value.handle || tr('chat.tool.subagent.handleMissing'),
      details,
      handle: value.handle || '',
      message: value.message || '',
      raw,
      invalidArguments
    };
  }
  return {
    kind: 'generic',
    label: name,
    target: '',
    details: [],
    raw,
    invalidArguments
  };
}

function normalizePlan(value) {
  value = normalizeJSONValue(value);
  if (!value || !Array.isArray(value.steps) || value.steps.length === 0) return null;
  const steps = value.steps
    .map((step) => ({
      title: String(step?.title || '').trim(),
      status: normalizePlanStatus(step?.status)
    }))
    .filter((step) => step.title);
  if (steps.length === 0) return null;
  return {
    title: String(value.title || '').trim(),
    note: String(value.note || '').trim(),
    steps
  };
}

function normalizePlanStatus(status) {
  const s = String(status || '').trim().toLowerCase();
  if (['pending', 'running', 'done', 'failed'].includes(s)) return s;
  return 'pending';
}

function formatToolResultSummary(toolName, summary, isError = false, tr = (k) => k) {
  if (isError) return summary;
  if (toolName === 'workflow_lint') {
    const parsed = parseWorkflowLintResult(summary);
    if (parsed) {
      if (parsed.valid) return `${tr('chat.tool.workflowLint.valid')} · ${parsed.status}`;
      return `${tr('chat.tool.workflowLint.invalid')} · ${parsed.error || parsed.status}`;
    }
  }
  return summary;
}

export function normalizeSessionMessage(message, tr = (k) => k) {
  if (message.role === 'toolCall') {
    const args = normalizeJSONValue(message.arguments);
    const plan = normalizePlan(message.plan || (message.toolName === 'plan' ? args : null));
    if (message.toolName === 'plan' && plan) {
      return {
        id: message.id,
        seq: message.seq,
        role: 'plan',
        agentId: message.agentId,
        toolCallId: message.toolCallId,
        toolName: message.toolName,
        plan
      };
    }
    return {
      id: message.id,
      seq: message.seq,
      role: 'toolCall',
      agentId: message.agentId,
      toolCallId: message.toolCallId,
      toolName: message.toolName || 'tool',
      arguments: args,
      invalidArguments: message.invalidArguments,
      callView: buildToolCallView(message.toolName || 'tool', args, message.invalidArguments, tr)
    };
  }
  if (message.role === 'toolResult') {
    if (message.toolName === 'plan' && !message.isError) return null;
    return {
      id: message.id,
      seq: message.seq,
      role: 'toolResult',
      agentId: message.agentId,
      toolCallId: message.toolCallId,
      toolName: message.toolName || 'tool',
      summary: formatToolResultSummary(message.toolName || 'tool', message.summary || tr('chat.tool.result'), message.isError, tr),
      isError: message.isError,
      hasDetail: message.hasDetail,
      detailLoaded: false,
      detailLoading: false,
      detailError: '',
      detail: null
    };
  }
  const images = [];
  for (const block of message.contents || []) {
    if (block.type !== 'image' || !block.image?.data || !block.image?.mimeType) continue;
    images.push({
      name: block.image.mimeType,
      type: block.image.mimeType,
      size: block.image.bytes || block.image.originalBytes || 0,
      dataUrl: `data:${block.image.mimeType};base64,${block.image.data}`
    });
  }
  return {
    id: message.id,
    seq: message.seq,
    role: message.role,
    agentId: message.agentId,
    content: message.content || textFromContents(message.contents),
    isError: Boolean(message.isError),
    images,
    attachments: normalizeAttachments(message.attachments)
  };
}

function normalizeAttachments(items) {
  if (!Array.isArray(items)) return [];
  return items.filter((item) => item && typeof item === 'object').map((item) => ({
    kind: String(item.kind || 'attachment'),
    name: String(item.name || ''),
    url: String(item.url || ''),
    mediaType: String(item.mediaType || ''),
    providerRef: String(item.providerRef || '')
  })).filter((item) => item.url || item.providerRef || item.name);
}

// --- list upserts ---

function shallowEqualMessage(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  const keys = Object.keys(a);
  if (keys.length !== Object.keys(b).length) return false;
  return keys.every((k) => {
    const va = a[k];
    const vb = b[k];
    if (va === vb) return true;
    // Object fields (images, plan, callView) are rebuilt on every replay;
    // compare them structurally so identical replays stay no-ops.
    if (va && vb && typeof va === 'object' && typeof vb === 'object') {
      return JSON.stringify(va) === JSON.stringify(vb);
    }
    return false;
  });
}

export function upsertMessageInList(list, next) {
  if (!next) return list;
  if (next.id) {
    const existing = list.findIndex((m) => m.id === next.id);
    if (existing >= 0) {
      const copy = [...list];
      copy[existing] = { ...copy[existing], ...next };
      return copy;
    }
  }
  if (next.role === 'toolCall' || next.role === 'plan') {
    const toolCallId = next.toolCallId || '';
    const existing = toolCallId ? list.findIndex((m) => m.toolCallId === toolCallId && (m.role === 'toolCall' || m.role === 'plan')) : -1;
    if (existing >= 0) {
      const copy = [...list];
      copy[existing] = { ...copy[existing], ...next };
      return copy;
    }
  }
  if (next.role === 'toolResult') {
    const toolCallId = next.toolCallId || '';
    const existing = toolCallId ? list.findIndex((m) => m.role === 'toolResult' && m.toolCallId === toolCallId) : -1;
    if (existing >= 0) {
      const copy = [...list];
      copy[existing] = { ...copy[existing], ...next };
      return copy;
    }
    const callIdx = toolCallId ? list.findIndex((m) => m.toolCallId === toolCallId && (m.role === 'toolCall' || m.role === 'plan')) : -1;
    if (callIdx >= 0) {
      return [...list.slice(0, callIdx + 1), next, ...list.slice(callIdx + 1)];
    }
  }
  return [...list, next];
}

// mergeOrInsertReplayMessage places a persisted (replay) message into the
// transcript in seq order, ahead of the live streaming tail (live messages
// carry no id/seq). When the live tail already holds the streaming copy of
// this message (same role, no id), the persisted version is merged into that
// copy instead of duplicating or reordering it.
function mergeOrInsertReplayMessage(view, next) {
  const seq = Number(next.seq || 0);
  let insertAt = view.messages.length;
  for (let i = 0; i < view.messages.length; i++) {
    const mSeq = Number(view.messages[i]?.seq || 0);
    if (mSeq === 0 || (seq > 0 && mSeq > seq)) {
      insertAt = i;
      break;
    }
  }
  for (let i = insertAt; i < view.messages.length; i++) {
    const candidate = view.messages[i];
    if (Number(candidate?.seq || 0) > 0) break;
    if (!candidate?.id && candidate?.role === next.role) {
      const merged = { ...candidate, ...next };
      if (shallowEqualMessage(candidate, merged)) return view;
      const messages = [...view.messages];
      messages[i] = merged;
      return { ...view, messages };
    }
  }
  return { ...view, messages: [...view.messages.slice(0, insertAt), next, ...view.messages.slice(insertAt)] };
}

function upsertTranscriptToolCallInView(view, next) {
  const toolCallId = next.toolCallId || '';
  const idx = toolCallId ? view.messages.findIndex((m) => m.toolCallId === toolCallId && (m.role === 'toolCall' || m.role === 'plan')) : -1;
  if (idx >= 0) {
    const messages = [...view.messages];
    messages[idx] = { ...messages[idx], ...next };
    return { ...view, messages };
  }
  if (next.id && Number(next.seq || 0) > 0) return mergeOrInsertReplayMessage(view, next);
  let messages = view.messages;
  const last = messages[messages.length - 1];
  if (last?.role === 'assistant' && !last.content && !last.images?.length) {
    messages = messages.slice(0, -1);
  }
  return { ...view, messages: [...messages, next] };
}

function upsertTranscriptToolResultInView(view, next) {
  const toolCallId = next.toolCallId || '';
  const existing = toolCallId ? view.messages.findIndex((m) => m.role === 'toolResult' && m.toolCallId === toolCallId) : -1;
  if (existing >= 0) {
    const messages = [...view.messages];
    messages[existing] = { ...messages[existing], ...next };
    return { ...view, messages };
  }
  const callIdx = toolCallId ? view.messages.findIndex((m) => m.toolCallId === toolCallId && (m.role === 'toolCall' || m.role === 'plan')) : -1;
  if (callIdx >= 0) {
    return {
      ...view,
      messages: [
        ...view.messages.slice(0, callIdx + 1),
        next,
        ...view.messages.slice(callIdx + 1)
      ]
    };
  }
  if (next.id && Number(next.seq || 0) > 0) return mergeOrInsertReplayMessage(view, next);
  let messages = view.messages;
  const last = messages[messages.length - 1];
  if (last?.role === 'assistant' && !last.content && !last.images?.length) {
    messages = messages.slice(0, -1);
  }
  return { ...view, messages: [...messages, next] };
}

export function upsertTranscriptMessageInView(view, next) {
  if (!next) return view;
  view = bumpCursorFromSeq(view, 'entrySeq', next.seq);
  if (next.id) {
    const existing = view.messages.findIndex((m) => m.id === next.id);
    if (existing >= 0) {
      const merged = { ...view.messages[existing], ...next };
      // Replay dedupe: identical merges keep the view untouched so callers
      // can skip rendering and scrolling for replayed history.
      if (shallowEqualMessage(view.messages[existing], merged)) return view;
      const messages = [...view.messages];
      messages[existing] = merged;
      return { ...view, messages };
    }
  }
  if (next.role === 'toolResult') {
    return upsertTranscriptToolResultInView(view, next);
  }
  if (next.role === 'toolCall' || next.role === 'plan') {
    return upsertTranscriptToolCallInView(view, next);
  }
  // Persisted replay messages merge into (or insert ahead of) the live
  // streaming tail; live messages append at the end.
  if (next.id && Number(next.seq || 0) > 0) return mergeOrInsertReplayMessage(view, next);
  return { ...view, messages: [...view.messages, next] };
}

export function appendAssistantDeltaToView(view, delta) {
  if (!delta) return view;
  const messages = [...view.messages];
  const last = messages[messages.length - 1];
  if (!last || last.role !== 'assistant') {
    messages.push({ role: 'assistant', content: delta });
  } else {
    messages[messages.length - 1] = { ...last, content: `${last.content || ''}${delta}` };
  }
  return { ...view, messages };
}

// --- sub-agents ---

export function subAgentStatusFromToolStatus(status, isError = false) {
  const s = String(status || '').toLowerCase();
  if (isError || s === 'error' || s === 'failed') return 'error';
  if (s === 'done' || s === 'completed' || s === 'complete') return 'done';
  if (s === 'destroyed') return 'destroyed';
  if (s === 'message_sent' || s === 'running' || s === 'ready') return 'running';
  return s || 'running';
}

export function ensureSubAgentInList(subAgents = [], agentID, patch = {}) {
  if (!agentID) return subAgents;
  const idx = subAgents.findIndex((item) => item.id === agentID);
  const now = new Date().toISOString();
  if (idx >= 0) {
    const copy = [...subAgents];
    copy[idx] = { ...copy[idx], updatedAt: now, ...patch };
    return copy;
  }
  return [...subAgents, {
    id: agentID,
    status: patch.status || 'running',
    active: true,
    messageCount: 0,
    startedAt: now,
    updatedAt: now,
    ...patch
  }];
}

function reduceSubAgentTranscriptMessage(view, message, type, tr) {
  const agentID = message?.agentId || '';
  if (!agentID) return view;
  let subAgents = ensureSubAgentInList(view.subAgents, agentID, { status: 'running' });
  const current = view.subAgentTranscripts[agentID] || [];
  if (type === 'assistant_delta') {
    const delta = message.content || '';
    if (!delta) return { ...view, subAgents };
    const next = [...current];
    const last = next[next.length - 1];
    if (last?.role === 'assistant') {
      next[next.length - 1] = { ...last, content: `${last.content || ''}${delta}` };
    } else {
      next.push({ role: 'assistant', agentId: agentID, content: delta });
    }
    subAgents = ensureSubAgentInList(subAgents, agentID, { status: 'running', messageCount: next.length });
    return { ...view, subAgents, subAgentTranscripts: { ...view.subAgentTranscripts, [agentID]: next } };
  }
  const normalized = normalizeSessionMessage(message, tr);
  if (!normalized) return { ...view, subAgents };
  const next = upsertMessageInList(current, normalized);
  subAgents = ensureSubAgentInList(subAgents, agentID, { status: 'running', messageCount: next.length });
  return { ...view, subAgents, subAgentTranscripts: { ...view.subAgentTranscripts, [agentID]: next } };
}

function reduceSubAgentStatus(view, agentID, status, summary = '') {
  if (!agentID) return view;
  const state = subAgentStatusFromToolStatus(status, status === 'error' || status === 'failed');
  const subAgents = ensureSubAgentInList(view.subAgents, agentID, {
    status: state,
    error: state === 'error' ? summary : ''
  });
  const current = view.subAgentTranscripts[agentID] || [];
  const next = upsertMessageInList(current, {
    id: `status:${agentID}:${state}:${summary}`,
    role: 'status',
    agentId: agentID,
    content: state,
    summary,
    isError: state === 'error'
  });
  return { ...view, subAgents, subAgentTranscripts: { ...view.subAgentTranscripts, [agentID]: next } };
}

function reduceSubAgentToolResultSummary(view, message) {
  if (!message || !isSubAgentTool(message.toolName)) return view;
  const result = parseSubAgentResult(message.content || message.summary || '');
  if (!result) return view;
  const handle = stringFrom(result.handle || result.id || result.agent_id || result.agentId);
  if (!handle) return view;
  const status = subAgentStatusFromToolStatus(result.status, message.isError);
  const patch = {
    status,
    lastResponse: stringFrom(result.result || result.last_response || result.partial_result || ''),
    error: stringFrom(result.error || '')
  };
  const messageCount = Number(result.message_count || 0);
  if (Number.isFinite(messageCount) && messageCount > 0) patch.messageCount = messageCount;
  return { ...view, subAgents: ensureSubAgentInList(view.subAgents, handle, patch) };
}

function reduceSubAgentToolStatus(view, item, tr) {
  const agentID = item?.agentId || '';
  if (!agentID) return view;
  let next = {
    ...view,
    subAgents: ensureSubAgentInList(view.subAgents, agentID, { status: subAgentStatusFromToolStatus(item.status, item.isError) })
  };
  if (item.status === 'running') {
    next = reduceSubAgentTranscriptMessage(next, {
      role: 'toolCall',
      agentId: agentID,
      toolCallId: item.toolCallId || '',
      toolName: item.tool,
      arguments: item.args
    }, 'message', tr);
  } else {
    next = reduceSubAgentTranscriptMessage(next, {
      role: 'toolResult',
      agentId: agentID,
      toolCallId: item.toolCallId || '',
      toolName: item.tool,
      summary: item.summary || (item.status === 'failed' ? tr('chat.tool.failed') : tr('chat.tool.completed')),
      isError: item.isError || item.status === 'failed',
      hasDetail: false
    }, 'message', tr);
  }
  return next;
}

// --- stream event reducers ---

// reduceTranscriptEvent applies a "transcript" stream frame (assistant_delta,
// message, subagent_status) to the view.
export function reduceTranscriptEvent(view, item, tr = (k) => k) {
  const effects = { scroll: true, forceScroll: false, subAgentRefresh: false, subAgentTranscriptAgent: '' };
  const message = item?.message;
  if (!message) return { view, effects: {} };
  if (message.agentId) {
    effects.subAgentTranscriptAgent = message.agentId;
    if (item.type === 'subagent_status') {
      effects.subAgentRefresh = true;
      return { view: reduceSubAgentStatus(view, message.agentId, message.content, message.summary || ''), effects };
    }
    return { view: reduceSubAgentTranscriptMessage(view, message, item.type, tr), effects };
  }
  if (item.type === 'attachments') {
    return { view: mergeAssistantAttachments(view, message.attachments), effects };
  }
  if (item.type === 'assistant_delta') {
    return { view: appendAssistantDeltaToView(view, message.content || ''), effects };
  }
  if (item.type === 'message') {
    if (message.role === 'user') {
      view = clearTransientStreamErrors(view);
    }
    let next = view;
    if (message.role === 'toolResult') {
      next = reduceSubAgentToolResultSummary(next, message);
      effects.subAgentRefresh = true;
    } else if (message.role === 'toolCall' && isSubAgentTool(message.toolName)) {
      effects.subAgentRefresh = true;
    }
    const normalized = normalizeSessionMessage(message, tr);
    next = upsertTranscriptMessageInView(next, normalized);
    if (normalized?.role === 'assistant' && normalized.isError && normalized.content) {
      effects.forceScroll = true;
    }
    return { view: next, effects };
  }
  return { view, effects: {} };
}

function mergeAssistantAttachments(view, items) {
  const attachments = normalizeAttachments(items);
  if (attachments.length === 0) return view;
  const index = [...view.messages].map((message) => message?.role).lastIndexOf('assistant');
  if (index < 0) {
    return { ...view, messages: [...view.messages, { role: 'assistant', content: '', attachments }] };
  }
  const messages = [...view.messages];
  const existing = messages[index]?.attachments || [];
  const seen = new Set(existing.map((item) => `${item.kind}\u0000${item.url}\u0000${item.providerRef}`));
  messages[index] = {
    ...messages[index],
    attachments: [...existing, ...attachments.filter((item) => {
      const key = `${item.kind}\u0000${item.url}\u0000${item.providerRef}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })]
  };
  return { ...view, messages };
}

// reduceToolStatusEvent applies a "tool_event" stream frame to the view.
export function reduceToolStatusEvent(view, item, tr = (k) => k) {
  const effects = { scroll: true, forceScroll: false, subAgentRefresh: false, subAgentTranscriptAgent: item?.agentId || '' };
  let next = { ...view, toolEvents: [...view.toolEvents.slice(-49), { type: 'tool', ...item }] };
  if (item?.agentId) {
    return { view: reduceSubAgentToolStatus(next, item, tr), effects };
  }
  if (!item?.tool || !item?.status) return { view: next, effects };
  if (item.status === 'running') {
    const normalized = normalizeSessionMessage({
      role: 'toolCall',
      agentId: item.agentId || '',
      toolCallId: item.toolCallId || '',
      toolName: item.tool,
      arguments: item.args
    }, tr);
    return { view: upsertTranscriptMessageInView(next, normalized), effects };
  }
  if (item.status === 'completed' || item.status === 'failed') {
    if (isSubAgentTool(item.tool)) {
      next = reduceSubAgentToolResultSummary(next, {
        toolName: item.tool,
        summary: item.summary || '',
        isError: item.isError || item.status === 'failed'
      });
      effects.subAgentRefresh = true;
    }
    if (item.tool === 'plan' && item.status !== 'failed' && !item.isError) {
      return { view: next, effects };
    }
    const normalized = normalizeSessionMessage({
      role: 'toolResult',
      agentId: item.agentId || '',
      toolCallId: item.toolCallId || '',
      toolName: item.tool,
      summary: item.summary || (item.status === 'failed' ? tr('chat.tool.failed') : tr('chat.tool.completed')),
      isError: item.isError || item.status === 'failed',
      hasDetail: Boolean(item.hasDetail && item.toolCallId)
    }, tr);
    return { view: upsertTranscriptMessageInView(next, normalized), effects };
  }
  return { view: next, effects };
}

// reduceRunEvent upserts a run lifecycle event and bumps the run cursor.
export function reduceRunEvent(view, event) {
  if (!event?.id) return view;
  let streamCompleted = view.streamCompleted;
  if (event.eventType === 'started' || event.status === 'running') streamCompleted = false;
  const idx = view.runEvents.findIndex((item) => item.id === event.id);
  const runEvents = idx >= 0
    ? view.runEvents.map((item, i) => (i === idx ? { ...item, ...event } : item))
    : [...view.runEvents, event];
  let next = { ...view, runEvents, streamCompleted };
  next = bumpCursorFromSeq(next, 'runSeq', event.seq);
  if (
    event.eventType === 'started'
    || event.status === 'running'
    || (event.eventType === 'finished' && event.status === 'completed')
  ) {
    return clearTransientStreamErrors(next);
  }
  return next;
}

// reduceCapabilityEvent upserts a capability change event.
export function reduceCapabilityEvent(view, event) {
  if (!event?.id) return view;
  const idx = view.capabilityEvents.findIndex((item) => item.id === event.id);
  const capabilityEvents = idx >= 0
    ? view.capabilityEvents.map((item, i) => (i === idx ? { ...item, ...event } : item))
    : [...view.capabilityEvents, event];
  const next = { ...view, capabilityEvents };
  return bumpCursorFromSeq(next, 'capabilitySeq', event.seq);
}

// reduceRuntimeSnapshot replaces the runtime snapshot.
export function reduceRuntimeSnapshot(view, snapshot) {
  return { ...view, runtime: snapshot };
}

// reduceStreamDone marks the stream as completed.
export function reduceStreamDone(view) {
  return { ...view, streamCompleted: true };
}

// reduceStreamError appends an assistant error message to the transcript.
export function reduceStreamError(view, message, tr = (k) => k) {
  const effects = { scroll: true, forceScroll: true };
  const text = String(message || '').trim();
  if (!text) return { view, effects: {} };
  const content = `**${tr('chat.taskFailed')}**\n\n${text}`;
  const messages = view.messages.filter((item) => !item?.transientError);
  const last = messages[messages.length - 1];
  if (last?.role === 'assistant' && !last.content && !last.images?.length) {
    messages[messages.length - 1] = { ...last, content, isError: true, transientError: true };
  } else if (!last?.content?.includes(text)) {
    messages.push({ role: 'assistant', content, isError: true, transientError: true });
  }
  return { view: { ...view, messages }, effects };
}

function clearTransientStreamErrors(view) {
  if (!view?.messages?.some((item) => item?.transientError)) return view;
  return { ...view, messages: view.messages.filter((item) => !item?.transientError) };
}

// reduceApprovalRequest folds an approval request into the runtime snapshot.
export function reduceApprovalRequest(view, item, sessionID) {
  const ownership = approvalRequestOwnership(sessionID, item);
  if (!ownership.belongs) return { view, applies: false };
  return { view: { ...view, runtime: applyApprovalRequestToRuntime(view.runtime, sessionID, item) }, applies: true };
}

// reduceApprovalResolved removes a resolved approval from the runtime snapshot.
export function reduceApprovalResolved(view, item) {
  const runtime = view.runtime
    ? { ...view.runtime, pendingApprovals: (view.runtime?.pendingApprovals || []).filter((approval) => approval.approvalId !== item.approvalId) }
    : view.runtime;
  return { ...view, runtime };
}
