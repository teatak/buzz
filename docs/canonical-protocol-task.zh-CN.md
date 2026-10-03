# 协议转换层实施计划

最后核对：2026-07-28

状态：代码实施完成，真实上游验证待执行

## 目标

Buzz 对外和对上游支持四种生成协议：

- `openai`：OpenAI Chat Completions
- `openai-responses`：OpenAI Responses
- `anthropic`：Anthropic Messages
- `gemini`：Gemini GenerateContent

当前 `gemini` 明确指：

- `models/*:generateContent`
- `models/*:streamGenerateContent`

Google 已将 GenerateContent 标记为 legacy，但仍承诺继续支持。新的 Interactions API
不是同一套线协议；后续如接入，应作为独立协议评估，不能混入现有 `gemini` 实现。

协议实现以官方文档为准，最后核对于 2026-07-28：

- [OpenAI Responses streaming events](https://platform.openai.com/docs/api-reference/responses-streaming)
- [Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)
- [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Gemini GenerateContent](https://ai.google.dev/api/generate-content)

最终目标：

1. 同协议请求直接透传。
2. 跨协议请求只经过一次入口转换和一次出口转换。
3. 四种协议完成全部 `12/12` 个跨协议方向。
4. 非流式和流式共用唯一的 Canonical 语义模型。
5. 无法无损表达的能力必须显式报错，禁止静默丢弃。
6. 协议转换不侵入鉴权、路由、Key 轮询、冷却和 usage 存储。

## 完成的定义

“存在 handler”或“普通文本能够返回”不代表协议方向已经完成。

每个跨协议方向必须分别通过以下验收：

| 能力 | 验收要求 |
|---|---|
| 非流式文本 | system/developer、连续多轮、finish reason 正确 |
| 多模态输入 | 图片、音频等已声明支持的输入能够转换 |
| 结构化输出 | JSON object、JSON schema 能转换或明确报错 |
| 非流式工具调用 | tools、tool choice、tool call、tool result 完整闭环 |
| 流式文本 | start、delta、done 顺序正确 |
| 流式工具调用 | tool start、arguments delta、tool done 顺序和 ID 稳定 |
| Reasoning | effort/level 正确映射，内容是否回流遵守产品策略 |
| Usage | prompt、completion、total、cached、reasoning token 正确 |
| 错误 | 上游状态码和错误结构转换为入口协议格式 |
| 真实上游 | 至少一个对应 provider 完成 smoke test |

项目进度只按这张验收表统计，不再使用主观百分比。

## 当前实现状态

图例：

- `透传`：同协议原始请求直接转发。
- `转换`：经过唯一 Canonical 模型完成请求和响应转换。

| 入口 \ 出口 | OpenAI Chat | OpenAI Responses | Anthropic | Gemini |
|---|---|---|---|---|
| OpenAI Chat | 透传 | 转换 | 转换 | 转换 |
| OpenAI Responses | 转换 | 透传 | 转换 | 转换 |
| Anthropic Messages | 转换 | 转换 | 透传 | 转换 |
| Gemini GenerateContent | 转换 | 转换 | 转换 | 透传 |

当前路由矩阵为 `16/16`，跨协议方向为 `12/12`。

已完成：

- 唯一 `Canonical*` 请求、响应、usage 和流事件模型。
- `12/12` 跨协议非流式路由。
- `12/12` 跨协议流式文本矩阵测试。
- OpenAI Chat、Responses、Anthropic 和 Gemini 的工具调用事件转换。
- OpenAI/DeepSeek reasoning、Responses reasoning、Anthropic thinking、Gemini thought signature。
- cached token 和 reasoning token usage 明细。
- OpenAI Chat/Responses JSON Schema 元数据和 Anthropic `output_config.format` 映射。
- 跨协议错误按入口协议格式返回。
- 跨协议未知字段和无法表达字段明确拒绝；同协议不受 Canonical 限制。
- Responses flat function tools、终态、refusal 和 incomplete details 已按当前协议映射。
- Anthropic cached usage、必填 `max_tokens` 和工具结果载荷已校验。
- Gemini function call/result 使用稳定 ID 配对，安全拦截不会被工具调用终止原因覆盖。
- 流式上游的 malformed/truncated 数据会显式失败，不再返回伪成功。

发布前仍需：

- 使用真实 Key 完成四类 provider smoke test。
- 继续扩充 `12/12` 方向的多模态、结构化输出和工具调用路由级用例。
- 验证客户端中断能够及时取消全部上游流。

## 目标架构

```text
Client
  |
  v
Inbound Handler
  |
  | inbound == outbound
  +--------------------------> Raw Passthrough
  |
  v
Inbound Decoder
  |
  v
Canonical Request
  |
  v
Outbound Encoder
  |
  v
Provider
  |
  v
Canonical Response / Stream Events
  |
  v
Inbound Protocol Encoder
  |
  v
Client
```

跨协议请求的固定转换次数：

- 请求：入口协议 -> Canonical -> 出口协议。
- 响应：出口协议 -> Canonical -> 入口协议。

禁止在 handler 中新增协议对协议的临时转换。

## 唯一 Canonical 模型

第一阶段必须收口成一套公开类型：

### CanonicalRequest

- `Model`
- `Messages`
- `Tools`
- `ToolChoice`
- `Temperature`
- `TopP`
- `MaxOutputTokens`
- `StopSequences`
- `ResponseFormat`
- `Reasoning`
- `Stream`
- `Extensions`

### CanonicalMessage

- `Role`：system、developer、user、assistant、tool
- `Name`
- `Parts`

### CanonicalPart

- text
- image
- audio
- tool call
- tool result
- reasoning

### CanonicalResponse

- `ID`
- `Model`
- `Message`
- `FinishReason`
- `Usage`
- `Extensions`

### CanonicalStreamEvent

事件类型固定为：

- response start
- message start
- text delta
- refusal delta
- reasoning delta
- tool call start
- tool arguments delta
- tool call done
- usage
- response done
- error

工具调用的 `index`、`call ID` 和 `name` 在整个流中必须稳定。

### CanonicalUsage

- `PromptTokens`
- `CompletionTokens`
- `TotalTokens`
- `CachedTokens`
- `ReasoningTokens`

## 转换接口

协议包最终提供按单个协议组织的能力，不提供两两 converter：

```go
type RequestDecoder interface {
	DecodeRequest(body []byte, meta RequestMeta) (CanonicalRequest, error)
}

type RequestEncoder interface {
	EncodeRequest(req CanonicalRequest) ([]byte, error)
}

type ResponseDecoder interface {
	DecodeResponse(body []byte, meta ResponseMeta) (CanonicalResponse, error)
}

type ResponseEncoder interface {
	EncodeResponse(resp CanonicalResponse) ([]byte, error)
}

type StreamDecoder interface {
	DecodeStream(r io.Reader, emit func(CanonicalStreamEvent) error) error
}

type StreamEncoder interface {
	EncodeStream(w io.Writer, events <-chan CanonicalStreamEvent) error
}
```

新增第五种协议时只新增该协议的 decoder/encoder，不新增四组两两转换。

## 不支持能力的策略

所有入口字段必须明确采用以下一种策略：

1. `map`：完整映射到 Canonical。
2. `extension`：保留到协议扩展字段，只允许同能力出口使用。
3. `reject`：返回明确的 unsupported 参数错误。

禁止依赖 `json.Unmarshal` 自动忽略未知字段作为兼容策略。

同协议透传不受 Canonical 能力范围限制。

## 实施顺序

### 阶段 0：校准基线

状态：已完成

- 记录真实协议矩阵。
- 取消“整体完成 94%”的主观口径。
- 明确完成定义和验收维度。
- 确认两套 Canonical 类型并存的问题。

### 阶段 1：收口 Canonical 类型

优先级：P0
状态：已完成（2026-07-28）

任务：

1. [x] 以当前实际能力为基线，定义唯一的 `Canonical*` 类型。
2. [x] 将正在使用的 `ChatRequest/ChatResponse/ChatStreamEvent` 迁移为 `Canonical*`。
3. [x] 合并 `chat.go` 与 `canonical.go` 的有效字段。
4. [x] 删除旧类型，未保留别名兼容。
5. [x] 给 Canonical 类型增加独立单元测试。
6. [x] 定义统一的 stream event 状态机。
7. [x] 将四种协议的现有流解码迁移到带类型事件。
8. [x] 验证工具调用 index、call ID、name 和 arguments delta 在流中稳定。

验收：

- `internal/protocol` 只存在一套 Canonical 请求、响应和流式事件。
- 四个协议 converter 全部使用这套类型。
- 同协议透传行为不变。
- `go test ./internal/...` 通过。

### 阶段 2：完善所有入口到 OpenAI 兼容出口

优先级：P0
状态：代码完成，真实 provider smoke test 待执行

范围：

- OpenAI Chat -> OpenAI Chat：保持透传。
- OpenAI Responses -> OpenAI Chat。
- Anthropic Messages -> OpenAI Chat。
- Gemini GenerateContent -> OpenAI Chat。

原因：DeepSeek、OpenRouter、Ollama、Mimo、Qwen、Kimi、GLM 等主要使用 OpenAI 兼容出口。

任务：

1. [x] 补齐三个跨协议方向的字段映射测试。
2. [x] 完成流式 tool call arguments 转换。
3. [x] 处理 DeepSeek 等 provider 的 reasoning 输出扩展。
4. [x] 统一 usage details。
5. [x] 统一错误响应转换。
6. [ ] 执行 OpenAI 兼容 provider smoke test。

验收：

- 三个跨协议方向通过完整能力表。
- DeepSeek 普通文本、流式文本、工具调用可实际使用。
- 不支持参数明确返回错误。

### 阶段 3：补齐缺失的非流式方向

优先级：P1
状态：已完成（2026-07-28）

先实现 Anthropic 原生出口：

1. OpenAI Chat -> Anthropic。
2. OpenAI Responses -> Anthropic。

再实现 Responses 原生出口：

3. OpenAI Chat -> OpenAI Responses。
4. Anthropic Messages -> OpenAI Responses。

验收：

- 协议路由矩阵达到 `16/16`。
- 跨协议非流式矩阵达到 `12/12`。
- 文本、结构化输出、非流式 function tool 和 usage 通过测试。

### 阶段 4：完成全矩阵流式转换

优先级：P1
状态：核心实现已完成（2026-07-28）

任务：

1. [x] OpenAI `tool_calls` delta。
2. [x] Responses `function_call_arguments` delta。
3. [x] Anthropic `input_json_delta`。
4. [x] Gemini `functionCall` stream。
5. [x] reasoning/thinking delta。
6. [x] usage、done 和 error 事件。
7. [x] 流读取和输出统一经过 Canonical stream event。

验收：

- `12/12` 跨协议方向均有流式文本测试。
- 支持 function tool 的方向均有流式工具调用闭环测试。
- 客户端断开能够取消上游请求。
- 不产生重复 delta、空工具调用或不稳定 call ID。

### 阶段 5：高级能力

优先级：P2
状态：按需实施

按实际需求逐项增加：

- OpenAI file input。
- Responses hosted tools、MCP、code interpreter。
- OpenAI custom tool。
- Anthropic URL/file image source。
- Gemini cachedContent。
- reasoning/thinking 内容回流。
- provider 特有扩展。

每项能力必须先扩展 Canonical 或明确使用 `Extensions`，再修改具体协议。

### 阶段 6：清理和发布

状态：进行中

任务：

1. [x] 删除旧 converter、重复类型和不再使用的 helper。
2. [x] 更新 README 的协议地址和能力矩阵。
3. [x] 更新 Admin UI 的协议能力说明。
4. [x] 跑本地完整测试。
5. [ ] 跑真实 provider smoke test。
6. [ ] 根据验收矩阵生成发布说明。

## 测试结构

### Converter 单元测试

每种协议分别测试：

- request -> Canonical
- Canonical -> request
- response -> Canonical
- Canonical -> response
- stream -> Canonical events
- Canonical events -> stream
- unsupported 参数

### 路由集成测试

每个跨协议方向至少覆盖：

- 非流式文本
- 流式文本
- 非流式工具调用
- 流式工具调用
- usage
- 上游错误

### 透传回归测试

四个同协议方向验证：

- body 不经过重编码。
- query 和必要 header 保留。
- 流式数据及时 flush。
- usage 仍被记录。

### 真实 provider smoke test

至少覆盖：

- 一个 OpenAI 兼容 provider。
- OpenAI Responses 原生。
- Anthropic 原生。
- Gemini 原生。

真实 Key 测试不进入默认测试流程，只在显式提供测试环境变量时执行。

## 本轮实施结果

1. `internal/protocol` 只保留一套 `Canonical*` 类型，旧 `chat.go` 已删除。
2. 四协议路由矩阵达到 `16/16`，跨协议方向达到 `12/12`。
3. 请求和响应均只经过一次 Canonical 转换，不存在协议对协议转换链。
4. Responses 与 Anthropic 流式输出编码器已补齐。
5. thought signature、reasoning/thinking 和 usage details 已进入 Canonical。
6. 错误响应由入口协议决定，同协议透传保持原始上游结构。
7. 未知字段和无法表达字段不再依赖 `json.Unmarshal` 静默忽略。
8. `12/12` 跨协议流式文本矩阵测试已通过。

代码实现已进入发布验证阶段；真实 provider smoke test 未完成前，不把协议兼容标记为最终发布完成。
