# 横向扩容评估与规划记录

记录日期：2026-10-02

状态：规划中，尚未实施

代码基线：[c12abcd043b3ba9795056ead4954cc8642eb6d18](https://github.com/teatak/buzzhive/commit/c12abcd043b3ba9795056ead4954cc8642eb6d18)

## 目标与边界

BuzzHive 从早期设计就应支持横向扩容，同时保留简单的单机 Docker 安装体验：

- 小规模部署可以继续使用一个 BuzzHive 实例。
- 需要扩容时，同一套程序可以在负载均衡器后运行多个副本，共享外部 PostgreSQL 和 Redis。
- 新增副本、重启和滚动升级不应改变鉴权、配置、会话和额度的正确性。
- 暂不要求拆分微服务或引入 Kubernetes；团队、任务等其他产品功能不属于本次评估范围。

本文只记录当前基础、问题、建议的改动范围和验收条件。优先级是规划建议，未排定实施时间，也不代表相关能力已经完成。

本次依据代码静态检查，没有启动双副本环境、执行测试或进行压测。因此不提供可支持用户数、吞吐量或硬件配置承诺。

## 结论

外置数据库、Redis 运行态和 PostgreSQL 事务为多副本部署提供了基础。但当前不能仅增加容器数量，就把部署视为已经具备可靠的横向扩容能力。

首先需要解决：

1. API Key、用户额度属性和 provider 运行态快照只在本进程刷新。
2. 空 API Key 快照会放行匿名请求；实例间快照不同会导致鉴权行为不同。
3. 会话注销、配置传播和依赖故障缺少完整的跨实例正确性约束。
4. 启动迁移、首次管理员创建、readiness 和优雅停机未形成多副本生命周期。
5. Usage 写入存在丢失路径；额度准入需要明确软上限还是硬上限。

应先通过双副本一致性与故障测试，再根据压测结果决定性能优化和更复杂的基础设施。

## 当前已有基础

### 外部 PostgreSQL 与 Redis

- `BUZZHIVE_DATABASE_URL` 已支持连接外部 PostgreSQL。
- Redis 可使用 `BUZZHIVE_REDIS_URL`，或 `BUZZHIVE_REDIS_ADDR`、`BUZZHIVE_REDIS_PASSWORD`、`BUZZHIVE_REDIS_DB` 配置。
- Redis 已用于后台会话、路由会话、工具签名和 Key 冷却状态。配置 Redis 后，启动时会检查连接。
- PostgreSQL 保存用户、API Keys、provider、模型、路由及 usage 等持久化数据。

来源：[配置加载][S1]、[Redis 运行态][S2]、[数据库初始化][S3]。

### 后台会话已经可以共享

后台登录使用随机 opaque token，配置 Redis 时多个实例可读取同一会话，并已有按用户撤销会话的索引和时间标记。无需为了扩容改成 JWT。

未配置 Redis 时，实际回退的是进程内 `adminSessions` map。README 中“回退到数据库”的描述与当前实现不一致，应在后续修正文档时一并处理。这个本地模式不能作为多副本共享会话方案。

来源：[会话读写与续期][S4]、[Redis 会话与撤销][S2]、[README 当前说明][S5]。

### 额度累加已经使用数据库事务

`InsertUsageBatch` 在同一 PostgreSQL 事务内写入 usage 明细、统计聚合和额度消耗。额度及统计采用数据库端加法更新和 `ON CONFLICT ... DO UPDATE`，并非各实例读取旧值后覆盖。

这一点已避免常见的并发累加覆盖问题。后续重点是准入语义、额度属性的新鲜度、事件幂等和失败恢复，不应把现有累加机制误判为完全没有并发保护。

来源：[Usage 事务及加法更新][S6]。

### 模型路由并非全部缓存在内存

`resolveRouteTargets` 调用 `ResolveModelRoutes` 查询 PostgreSQL，模型、route、provider 和 endpoint 的启用条件会在查询中参与过滤。

需要跨副本同步的是另外几类本地快照：`authTokens`、provider client registry、provider Key 列表等。路由选择的本地轮询游标也不等同于数据库里的路由定义。

来源：[请求路由解析][S7]、[路由 SQL][S8]、[启动时快照加载][S9]。

## 主要问题与改动方向

### 1. API 鉴权与用户状态：P0

当前 `authTokens` 在启动或本实例 reload 时读取，管理员修改后只刷新处理该操作的实例。其他实例可能继续接受已撤销 Key，或无法识别新 Key。用户禁用、额度调整和额度周期重置也会受到快照过期影响。

更关键的是：`authenticate` 在 `len(s.authTokens) == 0` 时返回有效的 `local` 用户。若 A 创建了第一个 Key，B 仍保留空快照，B 仍可能接受匿名请求；撤销最后一个有效 Key 后也需验证空快照行为。

建议范围：

- [ ] 将匿名开发模式与“数据库没有有效 Key”明确分离；部署模式默认拒绝匿名请求。
- [ ] 鉴权读取当前权威 API Key 和用户状态，或先核验持久化版本再使用缓存。
- [ ] 定义安全变更的生效边界：变更成功后的新请求不得继续使用已撤销的身份。已在途请求的处理规则另行明确。
- [ ] 鉴权依赖不可用时返回明确错误，不得退化成匿名放行。
- [ ] 覆盖新建首个 Key、撤销最后一个 Key、禁用用户、修改额度和重置周期。

不能仅靠 Redis Pub/Sub 通知证明撤销正确：实例离线或断线时会漏消息，必须能从持久化状态恢复并发现过期缓存。

来源：[API 鉴权][S10]、[AuthToken 查询][S11]、[本地 reload][S12]。

### 2. Provider 配置与 Key 快照：P0

provider client registry 与 provider Key 列表保存在进程内。新增 provider、修改 endpoint、轮换/停用 Key、自动停用异常 Key 后，其他副本可能继续使用旧快照。即使路由 SQL 已读到新定义，也可能与旧 client 或 Key 列表组合使用。

建议范围：

- [ ] 在 PostgreSQL 中引入可持久化的配置版本；配置变更与版本递增必须在同一事务提交。
- [ ] 实例读取一致版本的配置并构造完整快照，校验成功后再原子替换本地引用。
- [ ] Redis 通知只用于加速刷新；启动、重连和定期版本检查负责补漏。
- [ ] 明确版本检查频率、最大允许陈旧时间、刷新失败处理，以及停用 Key 的安全要求。
- [ ] 自动停用异常 Key 也走同一版本更新流程。
- [ ] 避免只在收到通知时 reload，或出现“数据库修改成功、版本没有更新”的状态。

来源：[启动快照][S9]、[reload][S12]、[配置读取][S13]、[异常 Key 停用][S14]。

### 3. 后台会话与 Redis 故障：P0

当前已有共享会话及按用户撤销机制，但还存在边界：

- logout 忽略删除会话的错误并返回成功。
- 续期使用无条件 SET；并发请求可能在另一请求注销并删除 token 后，把旧会话重新写入。
- 会话中存储用户信息快照，不能只凭旧快照认定当前角色和 `valid` 仍有效。
- Key 冷却、路由会话和工具签名的部分 Redis 错误会被忽略，或回退为本地状态，跨实例语义会随故障变化。

建议范围：

- [ ] 多副本模式要求配置共享 Redis，并阻止静默使用本地会话模式。
- [ ] 注销、续期和撤销建立原子条件，保证注销后的 token 不被并发续期复活。
- [ ] 对角色、用户有效状态或会话版本核验当前权威状态，补足撤销失败时的防线。
- [ ] 明确区分缓存未命中、依赖故障和数据损坏；为每类运行态规定拒绝或降级策略，并提供指标。
- [ ] 多实例 Key 冷却计数如需一致，使用原子操作替代 GET 后 SET，并测试恢复/清理与新冷却的竞态。
- [ ] 高可用部署选择可用的 Redis 高可用入口并验证故障切换。当前使用普通 `redis.Client`，不能默认认为已支持 Sentinel 或 Cluster 的发现与切换。

会话共享不依赖负载均衡粘性会话；粘性会话也不能替代上述正确性工作。

来源：[logout][S15]、[续期与注销][S4]、[Redis 运行态][S2]、[冷却处理][S16]、[路由会话][S17]、[工具签名][S18]。

### 4. 额度准入与计量：先明确产品语义

当前额度检查是在请求开始前确认仍有余额，没有预留本次请求成本。多个并发请求可以同时通过，单个长请求也可能超过剩余额度；这在单副本并发下同样存在。

同时，`AuthToken` 快照带有额度上限和 `quota_anchor_at`。旧快照可能把刚改成受限的用户仍当作无限额度，或把消耗记入旧周期。即便总额采用正确的原子累加，过期属性仍会产生错误账目。

建议先完成以下共同项：

- [ ] 使用权威的额度属性，定义额度修改、重置和跨周期请求的计量归属。
- [ ] 请求捕获明确的额度策略版本/周期，结算时按选定规则处理，不能从过期身份快照随意推导。
- [ ] 为额度检查失败、结算失败和跨副本并发建立测试。

额度准入有两种待选方向：

**软上限**

允许已准入请求完成，余额耗尽后拒绝新请求；明确并发和长请求可能产生超额，配合并发限制与可观察的超额指标。接受何种超额范围需要产品决定。

**硬上限**

若产品要求严格控制预算，需要短事务原子预留、请求完成后结算、失败或取消后释放，以及超时预留恢复。预留必须基于可强制执行的最大请求成本，覆盖输入、输出、协议支持和上游计费差异；若无法建立可靠上界，就不能承诺绝不超额。

不能在整个模型请求或 SSE 流期间持有数据库事务/行锁。硬上限会增加复杂度，应在确有需求时实施，而非为了“支持多副本”默认加入。

来源：[准入检查][S19]、[额度状态][S20]、[记账使用的身份属性][S21]、[数据库累加][S6]。

### 5. Usage 持久化与失败恢复：按用途定级

当前有明确的丢失路径：

- 无额度限制用户通常进入容量 1024 的内存队列，队列满时直接丢弃记录。
- 有额度限制用户同步写入，但写入失败只记录日志。
- 异步批量写入失败后清空当前 batch，没有持久化重试或恢复。
- 进程被终止时，尚在队列或 batch 中的记录可能丢失。

如果 usage 只是尽力而为的观察数据，需要明确这一语义并统计丢失量；如果用于扣费、额度账本或审计，应作为上线前 P0：

- [ ] 建立唯一 usage event ID，并区分逻辑请求、上游 attempt 与最终结算事件。
- [ ] 明细、聚合与额度扣减在同一事务中幂等应用，重复事件不会重复计数或扣减。
- [ ] 提供持久化接收、重试、积压恢复及对账路径，明确进程崩溃和数据库中断时可恢复的边界。
- [ ] 对流已返回但记账失败、最终 usage 未收到等情况规定处理策略，不能把未知 usage 静默当作零。
- [ ] 暴露队列深度、写入延迟、失败、丢弃与待恢复数量。

单纯把同步改成异步，或增加一次内存重试，不能解决可靠记账。

来源：[队列容量][S9]、[usage 记录与队列满处理][S21]、[批量 writer][S22]、[事务写入][S6]。

### 6. 启动、迁移与滚动升级：P0

每个进程在 `OpenStore` 时执行 `EnsureSchema`。当前迁移虽然在事务内执行，但没有迁移版本记录或跨进程迁移锁，且包含 DROP COLUMN 等破坏性 DDL。多副本同时启动、不同版本短暂共存时需要专门设计。

首次管理员创建采用“COUNT 判断是否需要 setup，再 INSERT”，也缺少跨实例原子化约束。

建议范围：

- [ ] 迁移加入版本记录及数据库级串行协调，或由独立一次性步骤执行。
- [ ] 应用启动校验 schema 版本，不让未完成初始化的副本进入 readiness。
- [ ] 为滚动升级明确允许共存的应用/schema 版本；破坏性变更采用分阶段发布，或明确要求维护窗口。
- [ ] 首次管理员创建在数据库事务内串行化，确保两个实例并发 setup 只成功一次。

项目早期可以不兼容历史数据和接口；若选择支持无中断滚动升级，仍需处理升级期间新旧实例共存的边界。

来源：[OpenStore][S3]、[EnsureSchema 与 DDL][S23]、[初始管理员][S24]。

### 7. 流量入口、停机与资源上限：P0 / P1

当前 `/health` 固定返回 200，只能证明 handler 能响应；`Run` 直接调用 `ListenAndServe`，没有 SIGTERM 后的 `Shutdown` 流程。usage writer 也没有退出/排空协议，不能只关闭 channel 就视为完成排空。

建议 P0：

- [ ] 区分 liveness 与 readiness。初始化未完成、关键依赖不可用或进入排空阶段时，应按服务策略退出流量接收。
- [ ] SIGTERM 后先摘除 readiness，再停止接收新请求，等待普通请求和 SSE 在配置期限内完成。
- [ ] 请求生产者停止后，显式排空 usage 队列及 batch，确认 writer 退出，再关闭 Redis/数据库连接。
- [ ] 协调负载均衡排空、应用 shutdown 超时与容器停止宽限期；超时仍未结束的流需有明确终止行为。
- [ ] 多副本部署由负载均衡器占用宿主机端口，BuzzHive 副本仅暴露容器内部端口。现有 Compose 固定映射 `9622`，不能原样通过 `--scale buzzhive=N` 扩容。
- [ ] 为 SSE 关闭代理响应缓冲，设置合适的流式超时；负载均衡器不得自动重放模型生成 POST，以免重复生成和计费。

进程硬崩溃时，正在传输的流仍会中断；另一个副本不能接续原 TCP/SSE 流。多副本解决新请求的可用性，不能承诺在途流无损迁移。

建议 P1：

- [ ] 显式配置数据库连接池上限、空闲连接和生命周期。副本数乘以单副本连接上限必须纳入数据库总预算。
- [ ] 按实例暴露活跃请求/流、配置版本、连接池等待、Redis 错误、usage 积压和上游错误。
- [ ] 区分单实例瞬时统计与数据库全局聚合，避免把内存计数展示为集群总数。
- [ ] 根据实测增加并发/请求体上限、背压和上游限流保护。
- [ ] 将 PostgreSQL/Redis 备份、恢复和故障切换纳入运维验证；应用多副本不能消除共享存储的单点。

来源：[health handler][S25]、[Run][S26]、[usage writer][S22]、[Compose][S27]、[数据库连接初始化][S3]。

## 建议实施顺序

1. 明确匿名模式、配置变更生效边界、软/硬额度，以及 usage 是否要求账本级可靠性。
2. 完成鉴权权威读取/版本校验、配置版本与完整快照刷新。
3. 补齐共享会话的注销/续期一致性及 Redis 故障策略。
4. 完成迁移协调、原子 setup、readiness、优雅停机和多副本入口配置。
5. 完成与选定产品语义匹配的额度和 usage 可靠性；如承担计费，不得推迟到扩容上线之后。
6. 执行双副本故障验收，再做容量测试与 P1 优化。

以上均为待实施建议；本次只提交这份记录。

## 双副本验收清单

测试环境：A、B 两个独立 BuzzHive 进程，共享 PostgreSQL/Redis，通过负载均衡器交替处理请求。测试应同时支持指定实例验证，避免粘性会话掩盖问题。

- [ ] A 创建首个 API Key 后，B 能识别新 Key，未携带 Key 的请求仍被拒绝。
- [ ] A 撤销 Key、禁用用户或撤销最后一个 Key 后，A/B 对新请求结果一致，不退化为匿名访问。
- [ ] A 修改 provider endpoint、轮换/停用 Key 后，B 在规定边界内切换；自动停用也能传播。
- [ ] 人为丢弃配置通知、断开订阅后重连、重启 B，仍能凭持久化版本发现并修复旧快照。
- [ ] route SQL 与 provider/Key 快照不会形成不可解释的混合版本。
- [ ] A 登录、B 访问后台成功；A 注销后 B 拒绝旧 token；并发续期不能复活已注销 token。
- [ ] 修改管理员角色/有效状态、撤销会话及 Redis 删除失败时，权限和错误结果符合约定。
- [ ] A/B 并发消耗同一额度，数据库累计正确；额度重置、无限转受限、周期边界符合选定规则。
- [ ] 软上限验证并发超额行为；若选硬上限，验证原子预留、结算、取消、超时回收和可执行成本上界。
- [ ] 重复投递同一 usage event、提交结果不确定后重试，不重复计数或扣减；队列满和数据库恢复不静默丢账。
- [ ] PostgreSQL/Redis 分别断开、超时及恢复，readiness、鉴权、配置和 usage 行为符合约定，不出现匿名放行。
- [ ] 路由会话、工具签名和冷却状态在交替实例及 Redis 故障下符合定义的降级策略。
- [ ] A/B 同时冷启动、同时首次 setup、执行迁移及滚动升级，没有重复初始化或未就绪接流量。
- [ ] 长 SSE 期间向 A 发送 SIGTERM，新请求转到 B，既有流按排空策略处理，usage 排空可核对。
- [ ] 硬杀 A 时明确记录在途流中断；负载均衡不自动重放请求，不产生重复生成和扣费。
- [ ] 新增副本后数据库总连接数、Redis 压力和上游请求量保持在配置预算内。

容量测试需另行记录实例资源、请求协议、输入/输出大小、流式占比、并发流数、上游延迟和配额、数据库/Redis 配置及错误率。根据这些结果评估扩容收益，不从代码结构推算“支持多少用户”。

## 代码依据

下列链接固定在本次评估的 commit，便于后续对照实现变化。

[S1]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/config.go#L27-L50
[S2]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/runtime_cache.go
[S3]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store.go#L14-L43
[S4]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/admin_api.go#L550-L655
[S5]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/README.zh-CN.md
[S6]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_usage.go#L10-L193
[S7]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/provider.go#L119-L139
[S8]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_provider.go#L284-L352
[S9]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/server.go#L13-L110
[S10]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/admin_api.go#L519-L548
[S11]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_users.go#L12-L35
[S12]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/admin_api.go#L1317-L1358
[S13]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_runtime.go
[S14]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/key_state.go
[S15]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/admin_api.go#L208-L212
[S16]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/key_cooldown.go
[S17]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/route_session.go
[S18]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/tool_signatures.go
[S19]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/quota.go
[S20]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_quota.go
[S21]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/usage.go#L49-L122
[S22]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/usage.go#L241-L266
[S23]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_schema.go
[S24]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/store_users.go#L274-L280
[S25]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/server.go#L113-L158
[S26]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/internal/run.go
[S27]: https://github.com/teatak/buzzhive/blob/c12abcd043b3ba9795056ead4954cc8642eb6d18/docker-compose.yml
