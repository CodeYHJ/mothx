# maxTokens 截断检测与升级机制调研报告

## 概述

本文档调研了 Qwen Code（Moark Code）中 `maxTokens` 参数的完整处理链路，包括：截断检测机制、max_token 处理逻辑、以及自适应升级机制。

---

## 1. 截断检测机制

截断检测的核心是统一使用 `FinishReason.MAX_TOKENS` 枚举值。不同内容生成器（Content Generator）从各自的 API 响应中提取结束原因，并映射到统一的 Gemini SDK `FinishReason` 枚举。

### 1.1 FinishReason 枚举定义

使用 Google Gemini SDK 的 `FinishReason` 枚举作为统一标准：

```typescript
// 来自 @google/genai
enum FinishReason {
  FINISH_REASON_UNSPECIFIED = 'FINISH_REASON_UNSPECIFIED',
  STOP = 'STOP',           // 正常结束
  MAX_TOKENS = 'MAX_TOKENS', // token 限制截断
  SAFETY = 'SAFETY',       // 安全过滤
  RECITATION = 'RECITATION',
  // ... 其他原因
}
```

### 1.2 Anthropic Content Generator

**文件位置**: `packages/core/src/core/anthropicContentGenerator/`

**检测流程**:

1. **API 响应解析**（`anthropicContentGenerator.ts:896-900`）:
   ```typescript
   case 'message_delta': {
     const stopReasonValue = event.delta.stop_reason;
     if (stopReasonValue) {
       finishReason = stopReasonValue;
     }
   }
   ```
   Anthropic SSE 流中的 `message_delta` 事件携带 `stop_reason` 字段。

2. **finish reason 映射**（`converter.ts:620-632`）:
   ```typescript
   mapAnthropicFinishReasonToGemini(reason?: string | null): FinishReason | undefined {
     if (!reason) return undefined;
     const mapping: Record<string, FinishReason> = {
       end_turn: FinishReason.STOP,
       stop_sequence: FinishReason.STOP,
       tool_use: FinishReason.STOP,
       max_tokens: FinishReason.MAX_TOKENS,  // ← 截断信号
       content_filter: FinishReason.SAFETY,
     };
     return mapping[reason] || FinishReason.FINISH_REASON_UNSPECIFIED;
   }
   ```

**关键点**: Anthropic API 原生使用 `max_tokens` 表示截断，直接映射到 `FinishReason.MAX_TOKENS`。

### 1.3 OpenAI-Compatible Content Generator

**文件位置**: `packages/core/src/core/openaiContentGenerator/`

**检测流程**:

1. **API 响应解析**（流式）:
   OpenAI 兼容 API 在 streaming chunk 的 `choice.finish_reason` 中返回结束原因。

2. **finish reason 映射**（`converter.ts:1380-1392`）:
   ```typescript
   function mapOpenAIFinishReasonToGemini(openaiReason: string | null): FinishReason {
     if (!openaiReason) return FinishReason.FINISH_REASON_UNSPECIFIED;
     const mapping: Record<string, FinishReason> = {
       stop: FinishReason.STOP,
       length: FinishReason.MAX_TOKENS,  // ← 截断信号
       content_filter: FinishReason.SAFETY,
       function_call: FinishReason.STOP,
       tool_calls: FinishReason.STOP,
     };
     return mapping[openaiReason] || FinishReason.FINISH_REASON_UNSPECIFIED;
   }
   ```

3. **工具调用截断检测**（`converter.ts:1282-1311`）:
   ```typescript
   let toolCallsTruncated = false;
   // ... 解析工具调用 ...
   toolCallsTruncated = toolCallParser.hasIncompleteToolCalls();
   
   // 如果工具调用 JSON 被截断，强制覆盖 finish_reason 为 'length'
   const effectiveFinishReason =
     toolCallsTruncated && choice.finish_reason !== 'length'
       ? 'length'
       : choice.finish_reason;
   ```

**关键点**: 
- OpenAI API 使用 `length` 表示截断（区别于 Anthropic 的 `max_tokens`）
- 额外检测工具调用的 JSON 截断情况，即使 `finish_reason` 不是 `length`，如果检测到不完整的工具调用也会强制标记为截断

### 1.4 Gemini Content Generator

**文件位置**: `packages/core/src/core/geminiContentGenerator/`

**检测流程**:

Gemini API 原生使用 `FinishReason.MAX_TOKENS`，无需映射，直接在响应中返回。

### 1.5 截断检测总结

| Content Generator | API 原生信号 | 映射到 FinishReason.MAX_TOKENS | 额外检测 |
|---|---|---|---|
| Anthropic | `stop_reason: "max_tokens"` | ✅ 直接映射 | 无 |
| OpenAI-Compatible | `finish_reason: "length"` | ✅ 映射 | 工具调用 JSON 截断检测 |
| Gemini | `finishReason: MAX_TOKENS` | ✅ 原生值 | 无 |

---

## 2. max_token 处理逻辑

### 2.1 参数入口与转换

**用户配置入口**:

1. **CLI 交互配置**（`acpAgent.ts:1371-1382`）:
   ```typescript
   const maxTokens = readPositiveNumber(
     record['maxTokens'],
     'advancedConfig.maxTokens',
   );
   ```

2. **Serve HTTP 接口**（`server.ts:542-570`）:
   ```typescript
   const maxTokens = parsePositiveBoundedInteger(
     rawAdvanced?.['maxTokens'],
     10_000_000,  // 上限 1000 万
   );
   ```

**类型转换**（`provider-config.ts:98-99`）:
```typescript
if (advCfg?.maxTokens && advCfg.maxTokens > 0) {
  cfg.samplingParams = { max_tokens: advCfg.maxTokens };
}
```

用户侧的 `maxTokens`（驼峰命名）转换为 API 层的 `samplingParams.max_tokens`。

### 2.2 三级优先级决策

核心逻辑在三个 content generator 实现中统一，位于 `applyOutputTokenLimit` 方法：

**Anthropic**（`anthropicContentGenerator.ts:584-614`）和 **OpenAI-Compatible**（`openaiContentGenerator/provider/default.ts:158-205`）使用一致的逻辑：

#### 优先级 1（最高）：用户显式配置

```typescript
if (userMaxTokens !== undefined && userMaxTokens !== null) {
  if (isKnownModel) {
    // 已知模型：取 min(用户值, 模型上限)，避免 API 错误
    maxTokens = Math.min(userMaxTokens, modelLimit);
  } else {
    // 未知模型：完全尊重用户值
    maxTokens = userMaxTokens;
  }
}
```

- **已知模型**（在 `OUTPUT_PATTERNS` 中定义）：取 `min(用户值, 模型上限)`，防止 `input + max_output > contextWindowSize` 导致 400 错误
- **未知模型**（自托管、部署别名）：直接使用用户值，后端可能支持更大限制

#### 优先级 2：环境变量

```typescript
const envVal = process.env['QWEN_CODE_MAX_OUTPUT_TOKENS'];
const envMaxTokens = envVal ? parseInt(envVal, 10) : NaN;
if (!isNaN(envMaxTokens) && envMaxTokens > 0) {
  maxTokens = isKnownModel
    ? Math.min(envMaxTokens, modelLimit)
    : envMaxTokens;
}
```

环境变量 `QWEN_CODE_MAX_OUTPUT_TOKENS` 提供固定覆盖，禁用自适应升级。

#### 优先级 3（默认）：自动策略

```typescript
maxTokens = Math.min(modelLimit, CAPPED_DEFAULT_MAX_TOKENS);
```

取 `min(模型输出上限, 8000)`，优化 GPU slot 预留。

### 2.3 关键常量定义

**文件位置**: `packages/core/src/core/tokenLimits.ts:18-19`

```typescript
export const CAPPED_DEFAULT_MAX_TOKENS: TokenCount = 8_000;  // 默认值上限
export const ESCALATED_MAX_TOKENS: TokenCount = 64_000;      // 升级重试值
```

**设计动机**:
- 99% 的响应在 5K tokens 以下
- 32K 默认值会导致 4-6 倍的 slot 过度预留
- 8K 默认值覆盖 99% 场景，被截断的 <1% 请求会升级到 64K 重试

### 2.4 参数传递链路

```
用户配置 advancedConfig.maxTokens
        ↓
provider-config.ts → samplingParams.max_tokens
        ↓
contentGenerator.applyOutputTokenLimit / resolveSamplingParams
  ├── 有 samplingParams → 用 min(用户值, 模型上限)
  ├── 无 samplingParams + 有环境变量 → min(env值, 模型上限)
  └── 都没有 → min(模型上限, 8000)
        ↓
API 请求体中的 max_tokens / maxOutputTokens 字段
```

---

## 3. 自适应升级机制（Adaptive Escalation）

### 3.1 升级触发条件

**文件位置**: `packages/core/src/core/geminiChat.ts:2313-2323`

```typescript
const shouldEscalateMaxOutputTokens =
  requestedMaxOutputTokens === undefined ||
  requestedMaxOutputTokens < escalatedLimit;

if (
  lastError === null &&                              // 无其他错误
  lastFinishReason === FinishReason.MAX_TOKENS &&    // 检测到截断
  !maxTokensEscalated &&                             // 未升级过
  !hasUserMaxTokensOverride &&                       // 用户未手动设置
  shouldEscalateMaxOutputTokens                      // 当前值小于升级上限
) {
  maxTokensEscalated = true;
  // ... 执行升级
}
```

**关键条件**:
1. `lastFinishReason === FinishReason.MAX_TOKENS` — 必须检测到截断
2. `!maxTokensEscalated` — 只升级一次，避免无限循环
3. `!hasUserMaxTokensOverride` — 用户未通过 config 或环境变量手动设置
4. `shouldEscalateMaxOutputTokens` — 当前请求的 `maxOutputTokens` 小于升级上限

### 3.2 升级上限计算

```typescript
const escalatedLimit = Math.max(
  ESCALATED_MAX_TOKENS,  // 64,000
  tokenLimit(model, 'output'),  // 模型原生输出上限
);
```

- 对于输出上限 < 64K 的模型：使用 64K
- 对于输出上限 ≥ 64K 的模型（如 Claude Opus 128K、GPT-5）：使用模型原生的上限

### 3.3 升级执行流程

**步骤 1: 清理历史**（`geminiChat.ts:2332-2339`）:
```typescript
// 从历史中移除不完整的模型回复
if (self.history.length > 0 &&
    self.history[self.history.length - 1].role === 'model') {
  self.history.pop();
}
```

**步骤 2: 通知 UI**（`geminiChat.ts:2341-2344`）:
```typescript
yield {
  type: StreamEventType.RETRY,
  maxOutputTokensEscalated: escalatedLimit,
};
```
UI 层接收到 `RETRY` 事件，丢弃部分输出，显示重试提示。

**步骤 3: 重新请求**（`geminiChat.ts:2346-2364`）:
```typescript
const escalatedParams: SendMessageParameters = {
  ...params,
  config: {
    ...params.config,
    maxOutputTokens: escalatedLimit,  // 使用升级后的上限
  },
};
const escalatedStream = await self.makeApiCallAndProcessStream(
  model,
  requestContents,
  escalatedParams,
  prompt_id,
);
for await (const chunk of escalatedStream) {
  const fr = chunk.candidates?.[0]?.finishReason;
  if (fr) escalatedFinishReason = fr;
  yield { type: StreamEventType.CHUNK, value: chunk };
}
```

### 3.4 恢复机制（Recovery）

如果升级后的响应仍然被截断（`escalatedFinishReason === FinishReason.MAX_TOKENS`），启动恢复循环：

**文件位置**: `packages/core/src/core/geminiChat.ts:2369-2493`

**关键常量**:
```typescript
const MAX_OUTPUT_RECOVERY_ATTEMPTS = 3;  // 最多恢复 3 次
const OUTPUT_RECOVERY_MESSAGE = 
  'Output token limit hit. Resume directly — no apology, no recap of what ' +
  'you were doing. Pick up mid-thought if that is where the cut happened. ' +
  'Break remaining work into smaller pieces.';
const OUTPUT_RECOVERY_TAIL_CHARS = 1200;  // 嵌入前一次回复的最后 1200 字符
```

**恢复流程**:

1. **跳过条件检查**（`geminiChat.ts:2380-2390`）:
   如果截断的 turn 包含 `functionCall`，跳过恢复，交由工具调度器的 fallback 处理（避免在 functionCall 和 functionResponse 之间插入无效的 user message）。

2. **注入恢复消息**（`geminiChat.ts:2400-2404`）:
   ```typescript
   self.history.push(
     createUserContent([
       { text: buildOutputRecoveryMessage(lastEntry) },
     ]),
   );
   ```
   
   `buildOutputRecoveryMessage` 构建包含前一次回复尾部的提示，指导模型从中断处继续：
   ```typescript
   function buildOutputRecoveryMessage(previousModelTurn: Content | undefined) {
     const previousText = /* 提取纯文本 */;
     const tail = previousText.slice(-OUTPUT_RECOVERY_TAIL_CHARS);
     return (
       `${OUTPUT_RECOVERY_MESSAGE}\n\n` +
       'The previous assistant response ended with this exact suffix. ' +
       'Do not repeat any line, table row, code line, or prose that already ' +
       'appears in it; output only text that comes after this suffix:\n\n' +
       '<previous_response_suffix>\n' + tail + '\n</previous_response_suffix>'
     );
   }
   ```

3. **通知 UI 继续**（`geminiChat.ts:2408`）:
   ```typescript
   yield { type: StreamEventType.RETRY, isContinuation: true };
   ```
   `isContinuation: true` 告诉 UI 保留已累积的文本缓冲区，将续写内容追加到之前的部分输出。

4. **重新请求**（`geminiChat.ts:2413-2423`）:
   使用更新后的历史（包含部分输出 + 恢复消息）重新发送请求。

5. **历史合并**（`geminiChat.ts:2490-2492`）:
   ```typescript
   if (successfulRecoveries > 0) {
     self.coalesceRecoveryPairs(successfulRecoveries);
   }
   ```
   将成功的恢复对（user recovery message + model continuation）合并回前一个 model turn，避免 `OUTPUT_RECOVERY_MESSAGE` 控制提示污染持久历史。

6. **错误处理**（`geminiChat.ts:2428-2482`）:
   如果恢复请求失败，回滚历史（弹出部分 model 回复和 recovery user message），发出合成的 `STOP` finish reason，终止流程。

---

## 4. Session 层截断处理

### 4.1 Session Token 限制

**文件位置**: `packages/cli/src/acp-integration/session/Session.ts:1682-1695`

Session 有独立的 token 总量限制（不同于单次请求的 `max_tokens`）：

```typescript
const sessionTokenLimit = this.config.getSessionTokenLimit();
if (sessionTokenLimit > 0) {
  const lastPromptTokenCount = this.#getPostCompressionTokenCount(compressionInfo);
  if (lastPromptTokenCount > sessionTokenLimit) {
    await this.#emitAgentDiagnosticMessageSafely(
      `Session token limit exceeded: ${lastPromptTokenCount} tokens > ${sessionTokenLimit} limit. ` +
        'Please start a new session or increase the sessionTokenLimit in your settings.json.',
      `Failed to emit token limit diagnostic for prompt ${promptId}`,
    );
    return { responseStream: null, stopReason: 'max_tokens' };
  }
}
```

### 4.2 Cron 任务禁用

**文件位置**: `packages/cli/src/acp-integration/session/Session.ts:2102-2110, 2202-2211`

当检测到 `stopReason === 'max_tokens'` 时，禁用 cron 任务：

```typescript
if (sendResult.stopReason === 'max_tokens') {
  this.#stopCronAfterTokenLimit();
}

#stopCronAfterTokenLimit(): void {
  this.cronDisabledByTokenLimit = true;
  this.cronQueue = [];
  if (!this.config.isCronEnabled()) return;
  this.config.getCronScheduler().stop();
  void this.#emitAgentDiagnosticMessageSafely(
    'Cron jobs disabled for the rest of this session due to token limit. Restart the session to re-enable.',
    'Failed to emit cron-disabled diagnostic',
  );
}
```

**设计动机**: 防止在 token 耗尽后 cron 任务继续触发 API 调用。

---

## 5. UI 层截断提示

**文件位置**: `packages/cli/src/ui/hooks/useGeminiStream.ts:176-178`

```typescript
if (message.includes('max_tokens') || message.includes('token limit')) {
  return 'max_output_tokens';
}
```

UI 层通过错误消息中的关键词识别 token 限制错误，显示相应的用户提示。

---

## 6. 完整数据流总结

```
┌─────────────────────────────────────────────────────────────┐
│ 1. 用户配置                                                   │
│    advancedConfig.maxTokens / QWEN_CODE_MAX_OUTPUT_TOKENS    │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. 参数转换                                                   │
│    provider-config.ts: maxTokens → samplingParams.max_tokens │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. 优先级决策 (applyOutputTokenLimit)                          │
│    ├─ P1: samplingParams.max_tokens (用户配置)               │
│    ├─ P2: QWEN_CODE_MAX_OUTPUT_TOKENS (环境变量)             │
│    └─ P3: min(modelLimit, 8000) (默认自动策略)                │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. API 请求                                                   │
│    发送请求体，包含 max_tokens / maxOutputTokens 字段          │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. API 响应解析                                               │
│    提取 finish_reason / stop_reason                           │
│    ├─ Anthropic: stop_reason: "max_tokens"                   │
│    ├─ OpenAI: finish_reason: "length" (+ 工具截断检测)        │
│    └─ Gemini: finishReason: MAX_TOKENS                       │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. 统一映射                                                   │
│    所有 finish reason → FinishReason.MAX_TOKENS              │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 7. 截断检测 (geminiChat.ts)                                   │
│    检查 lastFinishReason === FinishReason.MAX_TOKENS         │
└────────────────────────┬────────────────────────────────────┘
                         ↓
              ┌──────────┴──────────┐
              │ 是否被截断？          │
              └──────────┬──────────┘
                    ↓           ↓
                   否           是
                    ↓           ↓
         ┌──────────┘    ┌─────┴─────────────────────────┐
         │               │ 8. 升级条件检查                  │
         │               │   ├─ 未升级过                   │
         │               │   ├─ 用户未手动设置              │
         │               │   └─ 当前值 < 升级上限           │
         │               └─────────┬───────────────────────┘
         │                         ↓
         │              ┌──────────┴──────────┐
         │              │ 满足升级条件？        │
         │              └──────────┬──────────┘
         │                    ↓           ↓
         │                   否           是
         │                    ↓           ↓
         │         ┌──────────┘    ┌─────┴────────────────┐
         │         │               │ 9. 执行升级            │
         │         │               │   清理历史             │
         │         │               │   通知 UI (RETRY)      │
         │         │               │   重新请求 (64K+)      │
         │         │               └─────────┬─────────────┘
         │         │                         ↓
         │         │              ┌──────────┴──────────┐
         │         │              │ 升级后仍被截断？      │
         │         │              └──────────┬──────────┘
         │         │                    ↓           ↓
         │         │                   否           是
         │         │                    ↓           ↓
         │         │         ┌──────────┘    ┌─────┴──────────────┐
         │         │         │               │ 10. 恢复循环         │
         │         │         │               │    注入恢复消息      │
         │         │         │               │    重新请求 (最多3次) │
         │         │         │               │    合并历史          │
         │         │         │               └──────────────────────┘
         ↓         ↓         ↓               ↓
┌─────────────────────────────────────────────────────────────┐
│ 11. Session 层处理                                            │
│     如果 stopReason === 'max_tokens':                         │
│       禁用 cron 任务                                          │
└─────────────────────────────────────────────────────────────┘
```

---

## 7. 关键代码位置索引

| 功能 | 文件路径 | 行号 |
|---|---|---|
| 常量定义 | `packages/core/src/core/tokenLimits.ts` | 18-19 |
| 参数转换 | `packages/core/src/providers/provider-config.ts` | 98-99 |
| Anthropic finish reason 映射 | `packages/core/src/core/anthropicContentGenerator/converter.ts` | 620-632 |
| OpenAI finish reason 映射 | `packages/core/src/core/openaiContentGenerator/converter.ts` | 1380-1392 |
| OpenAI 工具截断检测 | `packages/core/src/core/openaiContentGenerator/converter.ts` | 1282-1311 |
| Anthropic max_tokens 决策 | `packages/core/src/core/anthropicContentGenerator/anthropicContentGenerator.ts` | 584-614 |
| OpenAI max_tokens 决策 | `packages/core/src/core/openaiContentGenerator/provider/default.ts` | 158-205 |
| 升级触发条件 | `packages/core/src/core/geminiChat.ts` | 2313-2323 |
| 升级执行流程 | `packages/core/src/core/geminiChat.ts` | 2332-2364 |
| 恢复循环 | `packages/core/src/core/geminiChat.ts` | 2369-2493 |
| 恢复消息构建 | `packages/core/src/core/geminiChat.ts` | 652-676 |
| Session cron 禁用 | `packages/cli/src/acp-integration/session/Session.ts` | 2202-2211 |
| UI 错误分类 | `packages/cli/src/ui/hooks/useGeminiStream.ts` | 176-178 |

---

## 8. 设计亮点

1. **统一的 FinishReason 抽象**: 所有 content generator 映射到 `FinishReason.MAX_TOKENS`，上层逻辑无需关心具体 API 差异。

2. **自适应升级策略**: 默认 8K 覆盖 99% 场景，优化 GPU slot 利用率；被截断时自动升级到 64K+，平衡成本与体验。

3. **智能恢复机制**: 升级后仍被截断时，通过注入恢复消息和保留历史，让模型从中断处继续，避免重复输出。

4. **工具调用保护**: 检测到工具调用截断时强制标记为 `MAX_TOKENS`，并跳过恢复循环（避免破坏 functionCall/functionResponse 序列）。

5. **一次性升级**: `maxTokensEscalated` flag 确保只升级一次，防止无限循环。

6. **用户控制优先**: 用户手动设置 `max_tokens`（通过 config 或环境变量）时，禁用自动升级，尊重用户意图。

---

## 9. 潜在改进方向

1. **更细粒度的升级策略**: 当前从 8K 直接升级到 64K+，可以考虑多级升级（如 8K → 16K → 32K → 64K），减少不必要的资源浪费。

2. **恢复消息优化**: 当前的 `OUTPUT_RECOVERY_MESSAGE` 是固定文本，可以根据截断位置（如代码块内、表格行中等）生成更具体的恢复提示。

3. **工具截断的主动处理**: 当前工具调用截断时跳过恢复，可以考虑在检测到工具 JSON 不完整时，主动尝试修复或提供 fallback 策略。

4. **Session token 限制的渐进式处理**: 当前 Session token 超限时直接丢弃请求，可以考虑先触发压缩（compression），给用户更多缓冲空间。

---

**文档版本**: 2026-07-25  
**调研范围**: Qwen Code (Moark Code) 全链路 maxTokens 处理机制
