# Claude 方案审视：AgentENV 运行到 Substrate 设计与实现方案评审

> 评审对象：《AgentEnv运行到Substrate可行性分析.md》（下称"可行性分析"）、《Substrate_AgentENV软件实现设计.md》（下称"实现设计"）。
> 评审基线：两个仓库的本地 checkout（Substrate `756c2a537` / v0.4.0，AgentENV `6cccaa7` / v0.2.3），并对两份文档声称的基线（Substrate `bb0effed`、AgentENV `8ff079c`）做了差异核对。
> 评审方法：静态源码核实。对两份文档中的关键技术断言逐条对照两个代码库的实际实现（含 file:line 级证据），并核对基线 commit 与当前 checkout 之间的演化（Substrate 侧 227 个提交、AgentENV 侧 9 个提交）。未进行编译、集成测试或性能测量——这与两份文档自身的诚实声明一致。

---

## 1. 总体结论

**方向可行，文档质量显著高于常见设计文档，核心架构判断经代码核实基本成立。但存在一个方案作者无法预见的结构性变化（上游已落地多 Actor Worker），它动摇了方案最核心的建模决策的事实前提；同时基线漂移已造成多处事实性失效，工程管理层面上还有明显缺口。**

具体判定：

| 维度 | 判定 | 说明 |
|---|---|---|
| 总体路线（新增 agentenv class、适配层优先、Substrate 唯一权威） | ✅ 可行，方向正确 | 与 Substrate 上游路线图（runtime modularity 列为优先级 6）吻合，与两个代码库的现实吻合 |
| 对两个代码库的事实描述 | ⚠️ 大体准确，但有漂移和少量错误 | 绝大多数断言经核实属实且证据充分；但 Substrate 侧 227 提交漂移已使 3-4 处核心断言失效（详见 §3.1） |
| "一 Worker 一 Actor"核心决策 | ⚠️ 需要重新论证 | 决策的事实前提（`actorsPerAteom=1`）在基线后 4 天被上游 #1836 推翻；该决策又直接决定 M2 最难组件（storage broker）的必要性和密度经济学的结局 |
| M1 范围与验收标准 | ✅ 现实 | 全量快照包 + Full scope + 单 Actor 起步是正确的风险排序；但 M1 的跨 Worker 恢复验收依赖设备问题先解决 |
| 快照所有权/GC 设计（M2/layer-catalog） | ✅ 思路正确，难度被如实承认 | 这是全方案正确性最难的部分，方案自己也承认"钩子未完成则 M2 必须关闭 GC" |
| 失败语义与 fencing | ⚠️ 方向正确，需按现状收窄 | S5 的 assignment_generation 提案经核实确有必要（协议链上无任何 fencing 字段）；但"上传失败保留 staged artifacts"的承诺超出当前 Substrate 语义 |
| 工程管理（工作量/排期/人力/上游策略） | ❌ 缺失 | 6 个新组件 + 9 个补丁系列的规模没有任何 sizing；与上游的协作策略缺失 |

**一句话结论：这份方案"能不能做"的答案是能，"现在要不要按此执行"的答案是需要先完成三件事——重新基线化、重做单/多 Actor Worker 决策 ADR、补齐工程量估计与上游协作策略。**

---

## 2. 方案做对了什么（先说优点，均有代码证据支撑）

这份文档的技术纪律值得肯定，以下判断经核实全部成立：

1. **诚实的边界声明。** 两份文档反复标注"拟新增/不是现成配置/未验证"，可行性分析明确"不把 README 数字当实测"、实现设计开篇声明"不代表已有可运行集成"。这在设计文档中少见。
2. **核心事实大多核实为准确**，包括几处相当深入的细节：
   - `SandboxSnapshotManifest` 路径字段全部 `#[serde(skip)]`，直接序列化不足以搬运快照（`AgentENV/src/sandbox/manifest.rs:63-94`）——属实；
   - `UblkDaemonClient` 创建即 spawn daemon、暴露 shutdown、无连接既有模式（`AgentENV/storage/ublk-daemon/src/client.rs:153-242,597-610`）——属实；
   - 网络槽必须从全局池领取、配置不可外部注入（`AgentENV/src/sandbox/firecracker/sandbox.rs:1863-1866,2098-2101`）——属实；
   - 两种调度策略忽略镜像 hint（`AgentENV/services/scheduler/internal/strategy.go:18-49`）——属实；
   - Checkpoint 契约 = 捕获后彻底 teardown 回 available（`substrate/internal/proto/ateompb/ateom.proto:39-41`、`cmd/ateom-microvm/checkpoint.go:216`），与 AgentENV snapshot 的"源继续运行"语义错配——方案对此的识别和处理（S6、M3 拆分 fork）正确；
   - Gateway 现状"仍调旧 Scheduler、转发旧 runtime"、集群列表全节点扇出且 all-or-nothing（`AgentENV/services/gateway/internal/cluster_list.go:189-198`）——属实；
   - Substrate 无通用 RBAC（按基线）、PG 事务绑定（actor_uid 主键唯一 + FOR UPDATE 行锁 + admit 容量重查 + 乐观锁）、Resume 分布式 lease——按基线核实属实（lease 在基线的 `atepg.go:1808` "Workflow leases" 段，后重构为独立文件）。
3. **状态权威设计正确。** "Substrate 是唯一 Assignment 权威、旧 Scheduler 不得形成第二权威、Gateway 数据面复用 atenet"的边界划分，与 Substrate 实际的 PG schema（`worker_assignments` 以 actor_uid 为主键）和工作流机制完全对齐。
4. **风险排序正确。** M1 全量快照包验证正确性 → M2 内容寻址共享层，是正确的风险递进；设备访问列为"上线阻断验证项"（§7.3/§18）被证明判断准确。
5. **故障注入测试清单（§17.1 的 10 条）质量很高**，直接命中了两个系统真实的薄弱处（幂等缝隙、fencing、设备复用、引用误删）。

---

## 3. 关键发现

### 3.1 P0-1【最重要】基线漂移已使方案的部分事实基础失效

**Substrate 当前 checkout（`756c2a537`，2026-10-07）领先文档基线（`bb0effed`，2026-09-20）227 个提交。** 漂移不是文档卫生问题——以下每一条都直接触碰方案的核心假设：

| 方案断言 | 基线时 | 当前 HEAD | 影响 |
|---|---|---|---|
| "一 Worker 一 Actor"（`actorsPerAteom=1`） | ✅ 属实（`internal/ateomcapacity/ateomcapacity.go:45`，注释 "One, today."） | ❌ **已被 #1836 "multi actor worker support"（2026-09-24）取代**：`--max-actors` 默认 1000，gvisor/microvm 均有完整的 `hosted.go` 多租户生命周期（准入计数、per-actor 网络 session、per-actor cgroup 叶子） | 动摇 M1 执行单元决策与 M2 broker 必要性论证，详见 3.2 |
| "调度在符合约束的候选中随机选取" | ✅ 属实（基线 `scheduling.go` 用 `math/rand.Intn`） | ❌ 已改为 **power-of-two-choices**（随机抽 2 个候选取负载较低者，`cmd/ateapi/internal/scheduling/scheduling.go:132-181`） | PlacementAdvisor 接入点（S8）需说明与新算法的组合关系；"缓存调度不存在"的论断仍成立 |
| "ate-api 尚未实现通用 Authorization/RBAC" | ✅ 属实 | ⚠️ **OpenFGA 鉴权已落地**（`cmd/ateapi/internal/authz/`，`--experimental-enable-authz` 门控，Actor/ActorTemplate CRUD 全覆盖，HEAD 提交即此项工作） | bridge 的作用域校验应评估对齐/复用 OpenFGA 模型，而非平行自建一套（§10.3 的 bridge 自有 schema 仍需要，但授权判定层可挂接） |
| （未提及）HardwareIdentity | 不存在 | ✅ **新增**：Worker 注册时上报硬件身份（`internal/hardware/hardware.go`、`internal/ateom/register.go:78`），`Matches()` 定义快照↔Worker 兼容判定；当前仅 architecture 属性，CPU vendor/model 是上游 TODO（注释明确提到"memory-restore matching"） | 方案的"CPU 兼容域用 WorkerPool 标签隔离"应改为**扩展该原生机制**——这正是上游为 Firecracker 式内存恢复预留的挂点，且 ateapi 调度约束目前完全没有硬件维度（`hardware.Matches` 尚无调用方） |
| （未提及）request parking | 不存在 | ✅ **新增**：atenet router 对饱和/瞬断的 Resume 请求挂起重试（默认 5s 预算，`docs/request-parking.md`） | bridge 的"创建 → Resume → await ready"语义可直接受益；方案 §4.5"无 Worker 时排队或拒绝"已被上游部分实现 |
| 快照 scope 语义 | on_pause 存在 | ⚠️ `SnapshotConfig.on_pause` **已移除**（#2309）：pause 也捕获 on_commit scope，Resume 按 `LocalSnapshot` 记录的 scope 恢复 | 方案 §1.1 拒绝清单中的 scope 术语需按当前语义重写（另见 3.3 的 DataOnGolden 问题） |
| Pause 语义 | — | 确认：**LOCAL pause 同样释放 Worker 槽位**（`workflow_pause.go:213` ReleaseActorFromWorker），只保留节点钉扎（AssignedNode→RequiredNodes）；代码 TODO 明言 pause/suspend 目前无实质差别 | 方案对此的理解（"清理执行并释放 Worker"）是**正确**的，列出仅为确认 |
| Worker 崩溃后的 Actor | — | 确认：**无自动 suspend/重调度**。Pod 消失 → RUNNING actor 直接 CRASHED（`workflow_worker_delete.go`）；容器重启 → epoch 抬升 → CRASHED。恢复靠 `RevertActor`（CRASHED→SUSPENDED）或对外部快照直接 Resume | 方案 §12 错误分类应显式增加"Worker 崩溃"入口及其 E2B 语义映射（沙箱只能回到最近持久快照） |

**建议行动：**
1. **立即重新基线化**：将两份文档 rebase 到当前 HEAD，并建立定期同步机制（上游 pre-1.0 且速度极快——17 天 227 提交，本次评审证明 17 天足以推翻核心前提）。
2. **评估上游协作策略**（详见 §6 行动清单）：Substrate 路线图明确将"runtime modularity（不同沙箱类型可插拔）"列为第 6 优先级，且 controller 中已有 TODO 承认"新 class 不应需要改 controller"（`workerpool_apply.go:422-426`）。方案 S1 的 class 泛化部分（把 pod shape 声明挪到 SandboxConfig、bump proto 枚举 maximum）正是上游愿意收的补丁类型。**私有 fork 维护 227 提交/17 天速度的上游，长期成本被方案低估。**

### 3.2 P0-2 "一 Worker 一 Actor"决策需要在多 Actor Worker 事实上重新论证

这是本次评审最重要的架构层发现。

**事实变化**：方案的决策依据是"当前 `actorsPerAteom = 1`，现有运行时、隧道及统计仍按单 Actor 工作"（可行性分析 §4.4），这在基线时属实。但基线后 4 天，上游落地了完整的多 Actor Worker 支持（#1836）：`hosted.go` 提供 per-actor 准入/网络/cgroup/统计，容量模型变为 `--max-actors`（默认 1000）。

**这不只是文档过时，它改变了两个核心权衡的结局：**

| 维度 | 单 Actor Worker（方案 M1/M2 现行选择） | 多 Actor Worker（上游已支持，AgentENV standalone 本来就是这种形态） |
|---|---|---|
| 内存设备/rootfs 层共享 | 必须 cross-Pod：M2 broker 要解决**动态 ublk 设备跨 Pod 的 device cgroup 放行**（方案自己列为上线阻断项） | 进程内共享，与 AgentENV standalone 完全一致（`UblkDeviceManager` 的进程内共享表直接可用），**broker 最难的部分消失** |
| 密度经济学（AgentENV 核心价值：生产 9.6x 内存超分、共享 page cache、ballooning） | 每 Worker 静态 requests/limits 按单 VM 峰值预留，节点密度 = Pod limit 之和；统计复用被 Pod 边界切断 | 一个 Pod 内统计复用多 VM，最接近 standalone 的超分表现 |
| Firecracker 预热池 | 跨 Worker 不能共享；单 Worker 内只有同 Worker 连续恢复才受益，价值边际 | 池跨 Actor 共享，恢复路径收益完整保留 |
| 与 Substrate 操作模型的贴合 | 完全贴合（Worker=最小执行单元、HPA 语义清晰） | 贴合度低一些，且**上游承认多 Actor 下 HPA 需要重新设计**（#1836 中明言 punted，当前 HPA demo 指标"at_capacity worker 计数"在多 Actor 下语义不成立） |
| 故障半径与运维 | 单 Actor 粒度滚动更新、爆炸半径小 | 一个 Worker 故障带走多个 Actor（但 AgentENV standalone 的 DaemonSet 形态本来如此） |
| 生命周期正确性（M1 目标） | 简单，无并发 Actor 状态交叉 | 需要处理 Worker 内多 Actor 并发操作——但上游 #1836 的 hosted 框架已提供 |
| 快照上传/带宽聚合 | 每 Worker 独立上传 | 同节点多 Actor 上传可聚合去重 |

**评审建议**：
- M1 保持单 Actor 仍是**合理的起步**（先验证生命周期正确性，方案的原有理由仍部分成立）。
- 但必须**在 M2 动工前完成一份单/多 Actor Worker 的 ADR**，基于当前 HEAD 的事实重算：M2 broker 的跨 Pod 设备共享是最难且可能被多 Actor 路线部分架空的组件；若多 Actor 路线胜出，broker 退化为节点级镜像缓存/P2P 目录服务，复杂度大幅下降，而密度经济学得以保全。
- 无论哪条路线，**M2 退出标准应增加量化密度指标**（如同节点内存超分比、同模板 Actor 共享内存设备的实际复用率），否则无法回答"整合后 AgentENV 的关键优势是否还在"这一方案自己提出的 PoC 决策标准。当前 §17.2 只测延迟和字节数，不测密度。
- 注意上游 HPA 在多 Actor 下未决——若选多 Actor，自动扩缩容信号需要自定义指标并接受上游再设计风险。

### 3.3 P0-3 快照上传失败语义：方案的承诺超出当前 Substrate 能力

实现设计 §6.2 承诺"上传失败必须保留节点 staged artifacts 并报告状态，不伪报 suspend 成功"。**当前事实**（`cmd/atelet/main.go:747` TODO #362）：EXTERNAL 上传失败 → **Actor 直接 CRASHED**，快照文件不保留复用（重试用确定性对象名覆盖，可能留孤儿对象）；manifest 最后写入作为提交标记（与方案 §8.4 的提交协议设计吻合——这部分方案理解正确）。LOCAL pause 的本地快照会保留在节点（但 pruneLocalCheckpoints 会清理全部本地快照）。

**建议**：M1 明确声明"EXTERNAL suspend 上传失败按现状 = CRASHED，恢复路径为 RevertActor + 从上一个持久快照 Resume"，把"staged artifacts 保留重试"作为 S7/M2 的显式新语义开发项（它确实需要 atelet/ateapi 改动，不是免费获得）。这与方案"失败优先产生可回收孤儿，不产生不可恢复快照"的存储原则并不冲突，但需要把现状写清楚，避免验收时按不存在的语义测试。

### 3.4 P0-4 fencing/操作身份：方案判断被证实，且是硬缺口

- 两个协议（`ateompb`、`ateletpb`）**均无 operation ID / assignment generation / epoch 字段**；唯一的 incarnation 防护是 `actor_uid`（防同名重建）；Resume 有 PG lease（30s TTL + 续约）+ 分步幂等工作流。
- atelet→ateom 的 Checkpoint 存在已知幂等缝隙（issue #372，代码 TODO："once atelet's Checkpoint is idempotent on those keys this step becomes fully reentrant"）——目前靠控制面状态机兜底。
- 方案 S5（传递 operation/assignment generation）与 §6.4 本地操作日志**确属必要**，不是过度设计。这是方案最扎实的部分之一。
- 补充建议：方案 §12.5 指出代际"不能单独阻止网络隔离节点上的旧 VM 继续运行"——正确。结合 3.1 的"Worker 崩溃→CRASHED"事实，评审建议在 §12.4 错误分类中显式增加 `WORKER_LOST` 类（对应 K8s Pod 消失/重启），并定义其 E2B 语义（外部可见 = 沙箱崩溃，只能从最近持久快照恢复），当前分类表中没有这个最常见的现实入口。

### 3.5 P0-5 设备访问（方案已识别为阻断项，补充关键事实）

方案的 §7.3/§18 判断准确，补充核实到的事实以缩小验证范围：

- **Substrate Worker 是非特权 Pod**：`drop ALL` + 有限 capability 集、KVM 通过 atelet 内置 device plugin 以扩展资源 `ate.dev/kvm` 申请、cgroup v2 委派（`internal/ateomcgroup`）、per-actor cgroup 叶子。`/dev/net/tun` 走 bind mount。**这是与 AgentENV 现状（DaemonSet `privileged: true` + hostPath `/dev` 全设备，`deploy/k8s/base/agentenv-daemonset.yaml:111-152`）的根本差距。**
- ublk daemon spawn 需要调用方持 CAP_SYS_ADMIN，daemon 被降权为仅 CAP_SYS_ADMIN；**daemon 用 pidfd 监视父进程、父退出即自杀**（`storage/ublk-daemon/src/main.rs:341-378`）——方案的 broker 监督设计必须同时解决生命周期解耦、能力模型、多客户端归属三个问题，方案 §5.1/§7.1 已覆盖，但建议把 pidfd 细节写进 broker 设计（这是"改个启动方式"假象下最容易踩的坑）。
- **好消息**：daemon 侧已存在跨连接的共享设备表 `active_shared`（引用计数，`storage/ublk-daemon/src/server.rs:142,1543`）——跨进程共享的**原语**已经存在，缺的是客户端协议（连接既有 daemon）、权限边界与设备 cgroup 放行。方案 §2 表中"原引用共享自动失效，需迁到节点 broker"略悲观了半步：客户端进程内表确实失效，但 daemon 侧复用能力尚在，broker 改造量比"重建共享机制"小。
- 动态创建的 ublk 设备（动态 minor）**无法**被 kubelet 设备管理在 Pod 准入后追加放行；预分配设备池 / CDI / DRA 是仅有的正路（方案 §7.3 列举正确）。注意若走 3.2 的多 Actor 路线，此问题大幅缩窄为"broker 自身 DaemonSet 的设备权限"，而非"每个 Worker Pod 的设备放行"。

### 3.6 P1 方案自身的错误与不精确处（即使按基线也应修正）

1. **LOCAL/EXTERNAL 的协议层次**（实现设计 §2 表、S6）：该区分存在于 **ateletpb 的 `CheckpointType`**（ateapi→atelet 层），Ateom 协议本身不知道快照去向（ateom 层只有 `SnapshotScope` FULL/DATA）。S6"下传已有 LOCAL/EXTERNAL 区别"实际是**新增 ateompb 字段的新工作**，不是既有字段透传——工作量表述应更准确。
2. **`DataOnGolden` 不存在**：实现设计 §1.1 拒绝清单中的 "Data/DataOnGolden"——当前 Substrate 代码与文档中只有 `Full`/`Data` 两种 scope（`docs/glossary.md`："Two scopes exist today"），DataOnGolden 在两个仓库均无对应物。应更正术语或注明出处。
3. **"自动恢复所有沙箱"描述不准**（实现设计 §5.1 表）：AgentENV 启动时只加载 Paused 元数据记录（不做 resume-all），恢复是代理流量惰性触发（`src/api/proxy.rs:721-736`，60s 上限）；**优雅停机时会 pause 所有运行中沙箱**（`orchestrator/service.rs:2784-2863`）——这个停机行为其实与 Substrate 的"SIGTERM 优雅 checkpoint + 3600s grace"路径天然对齐，是方案可以显式利用的机制（当前方案未提）。
4. **调度文件引用**：`BindActorToWorker` 在基线时确在 `atepg.go`（后被 #1777 拆分为 `worker_assignment.go:72`），文档引用按基线没错，rebase 时需更新。
5. **CPU 求交机制描述**：可行性分析 §4.3 说"CPU 配置求交与返回 → 后端能力/兼容域服务"——核实属实且方案遗漏了一个细节：求交结果经 `HeartbeatResponse.cpu_config_json` 下发、节点写入全局 `RwLock` 供 `FirecrackerSandboxFactory` 使用（`src/bin/server.rs:112-128`）。即 AgentENV 的**节点侧消费端已经是全局单例注入**，嵌入 Worker 后该链路要重新设计为 adapter 注入——方案 §5.2 的 `ExecutionContext` 设计正确覆盖，但建议明确点名这条链路。

### 3.7 P1 方案遗漏的现有机制（应优先复用而非重造）

| 遗漏机制 | 位置 | 对方案的意义 |
|---|---|---|
| `RequestActorSuspend`（Worker 观测空闲 → 请求控制面 suspend，"worker observes, control plane decides"） | `internal/ateomsuspend/`、`cmd/atelet/ateomsupport.go:170-210` | 方案 §10.2 的 timeout/自动暂停除 bridge 的 wall-clock `deadline_jobs` 外，还应挂接此机制做空闲回收；两者语义（墙钟超时 vs 空闲检测）应显式区分 |
| request parking（router 对饱和/瞬断 Resume 挂起重试，5s 预算） | `cmd/atenet/internal/router/` | bridge "创建 → Resume → await ready" 与 E2B 客户端重试语义的直接支撑 |
| `RevertActor`（CRASHED→SUSPENDED） | `workflow_revert.go` | 错误恢复路径的现成原语（3.1/3.4 已述） |
| `UploadPausedCheckpoint`（paused actor 本地快照 → 对象存储，不驱动 ateom） | `atelet.proto:193-197` | 方案 §12.3 第 4 步"LOCAL 保留 → 后续 EXTERNAL 提交"应复用此 RPC 而非新设计 |
| HardwareIdentity / `hardware.Matches` | `internal/hardware/` | CPU 兼容域的正确挂点（3.1 已述）；配合 AgentENV 的 `IntersectCpuConfigs`（`services/scheduler/internal/cpu_template.go:45-77`）可给出完整方案 |
| OpenFGA authz（实验） | `cmd/ateapi/internal/authz/` | bridge 授权层对齐点（3.1 已述） |
| envd token 重绑定机制 | `AgentENV/src/sandbox/firecracker/sandbox.rs:724-755`、`config.rs:176-179` | 方案 §5.3/§18 把"envd/guest 身份可安全重绑定"列为原型验证项并预期"可能需要新增初始化通道"——实际 AgentENV 已有相当完整的实现：token 不序列化（serde(skip)）、恢复时 `from_snapshot_config_with_override` 换新身份并改写 MMDS、`wait_for_ready` 重新执行 envd init（"including after restore"）。**该风险低于方案预期**，验证工作应聚焦"跨节点 seed 共享"与 fork 场景，而非从零建通道 |

### 3.8 P1 覆盖缺口：出站域名策略与 E2B 数据面契约细节

1. **出站策略映射缺失**：AgentENV 的 `allowOut/denyOut/basePolicy` + 域名白名单通过 per-netns 透明代理（iptables REDIRECT → :15000，HTTP Host/TLS SNI 嗅探，`src/sandbox/network/egress_proxy.rs`）实施；Substrate 的出站走 atunnel egress（atenet egress gateway/mitm）。E2B SDK 用户依赖域名白名单语义，方案 §9 的网络设计只说"复用 Substrate 出站机制"，**没有给出 E2B 域名策略 → Substrate egress policy 的映射设计**。建议列入 M3 前的必答项（至少 M1 声明不支持并明确拒绝）。
2. **E2B 数据面是公开 header 契约**：`{port}-{sandboxID}.{proxy_domain}` 泛域名路由与 `e2b-sandbox-id`/`e2b-sandbox-port`/`x-agentenv-*` header 协议被 E2B SDK 和 aenv CLI（含免 `/proxy` 前缀的 Connect-RPC 通道）依赖；gateway 数据面不鉴权是**刻意设计**（策略在节点，`services/README.md` 明确）。bridge/atenet 兼容层必须完整保留 header 语义（含 URL 转义、`%2F` 不误解码——方案 §9.3 已提）+ 泛域名证书方案（每个端口一个子域 → CONNECT authority/目标端口的转换）。建议把"header 协议契约测试"单列为 P5 验收项。

### 3.9 P2 其他建议

1. **Firecracker fork 依赖应升格为显式风险登记**：内存快照链路依赖 kvcache-ai fork（`1.15.1-patch-v1`：`getDirtyMemoryRanges`、`track_dirty_pages`、memory hotplug、balloon hinting 等定制 API）+ overlaybd fork（`v1.0.18-aenv.1`）+ 定制 guest kernel（6.1.175）。快照格式与 fork 版本绑定，方案用 `runtimeAssetsDigest` 进快照 manifest 应对了兼容性识别（正确），但 fork 跟进上游 Firecracker 安全更新的长期维护成本应写入风险登记册。
2. **Go↔Rust UDS gRPC 的关键路径开销**应纳入 P2 验收测量项（预计亚毫秒级、不构成风险，但 restore 延迟是核心指标，应有数据）。
3. **GPU 完全未提及**：agent 工作负载趋势上 GPU 需求显著，两个系统当前都不支持 GPU 透传。至少应在"首期拒绝"清单中声明范围外。
4. **威胁建模**：Substrate 有 `docs/threat-model.md`，方案的 bridge/broker 涉及新的信任边界（外部 E2B 凭据、节点共享挂载、UDS 多租户），应做一轮对齐 Substrate 威胁模型的威胁建模。方案 §7.1 对 UDS/共享挂载攻击面的讨论已有雏形。
5. **性能基线**：可行性分析 §6 阶段 1 的"原生基线对比"正确，建议补一项——**同节点多 VM 密度与内存超分对比**（standalone vs Substrate 形态），这是 3.2 决策的数据输入。

---

## 4. 事实核对明细

### 4.1 Substrate 侧（方案断言 → 核实结果）

| # | 方案断言 | 结果 | 证据（substrate/ 下） |
|---|---|---|---|
| 1 | sandboxClass 仅 gvisor/microvm，校验分散 | ✅ 属实 | CRD Enum 两处（`workerpool_types.go:113`、`sandboxconfig_types.go:61`）+ CEL + proto 枚举（`ateapi.proto:935`，**带 `maximum=2` 校验注解——新增 class 必须同步 bump 并重新生成**，易漏）+ ValidatingAdmissionPolicy + converter/metrics/prewarm/controller 的 switch + ate-setup 镜像列表，约 10 处 |
| 2 | WorkerPool 支持 workerImage | ✅ 属实（且为必填） | `workerpool_types.go:94-97` |
| 3 | `actorsPerAteom = 1` | ⚠️ 基线属实、**现已失效** | 基线 `internal/ateomcapacity/ateomcapacity.go:45`；HEAD 已删除，#1836 多 Actor 支持（`--max-actors` 默认 1000，`cmd/ateom-microvm/hosted.go`） |
| 4 | 调度为随机选取 | ⚠️ 基线属实、现为 power-of-two-choices | `scheduling.go:132-181` |
| 5 | BindActorToWorker 事务（唯一绑定/容量重查） | ✅ 属实 | `atepg/worker_assignment.go:72-186`（基线时在 atepg.go）：actor_uid 主键、FOR UPDATE 行锁、admit 回调重查、乐观锁 |
| 6 | Resume 有分布式 lease、分步恢复 | ✅ 属实 | `workflow_resume.go:99`（acquireActorLease）、atepg lease 表（30s TTL+续约）；每步幂等可重入 |
| 7 | 本地 pause / 持久 suspend 两种操作 + RequiredNodes | ✅ 属实 | `workflow_pause.go:182-299`（LOCAL+保留 AssignedNode）、`workflow_suspend.go`（EXTERNAL+清空）、`workflow_resume.go:558-573`（AssignedNode→RequiredNodes，"never relaxed"） |
| 8 | Checkpoint 释放执行实例、worker 回 available | ✅ 属实 | `ateom.proto:39-41`、`ateom-microvm/checkpoint.go:216` |
| 9 | Checkpoint 返回相对文件清单；atelet 写 manifest.json | ✅ 属实 | `ateom.proto:304-313`、`sandbox_assets.go:52,83-112`；EXTERNAL 上传 manifest 最后写（提交标记） |
| 10 | WorkloadSpec 只有容器名/探针/挂载，无镜像/env/命令 | ✅ 属实 | `ateom.proto:180-203`；镜像信息由 atelet 烘焙进 OCI bundle（`oci.go`） |
| 11 | Ateom 协议支持 LOCAL/EXTERNAL | ⚠️ 层次有误 | LOCAL/EXTERNAL 是 **ateletpb** `CheckpointType`；ateom 层只有 FULL/DATA scope |
| 12 | 协议有 operation ID / fencing | ❌ 不存在（方案 S5 正确识别为缺失） | 两 proto 均无；唯一 incarnation 防护是 actor_uid |
| 13 | microVM 走 device plugin 申请 KVM、非特权 | ✅ 属实 | `internal/deviceplugin`（`ate.dev/kvm`）、`workerpool_apply.go:374-416`（privileged:false + caps + AppArmor/seccomp Unconfined）、cgroup 委派 `internal/ateomcgroup` |
| 14 | CSI 不经 PVC、DurableDir 语义 | ✅ 属实 | CSI 经 virtio-fs 共享目录（非块设备）；Data scope = 每卷一个 tar |
| 15 | 入站仅 HTTP(S) 隧道、非裸 TCP | ✅ 基本属实 | `:443` mTLS HTTPS 反代 + `:8443` mTLS CONNECT（任意 TCP **载荷**可经 CONNECT 中继，但入口协议是 HTTP CONNECT over TLS，无裸 TCP 监听） |
| 16 | atunnel/atunnel 证书机制 | ✅ 属实 | atunnel 是**编译进 ateom 的进程内库**（非 sidecar，`internal/ateomtunnel/ateomtunnel.go:17-19`）；pod 证书每连接重载；actor 证书经同节点 atelet 铸造（私钥不出 atunnel） |
| 17 | ate-api 无通用 RBAC | ⚠️ 基线属实、现有实验性 OpenFGA | `cmd/ateapi/internal/authz/` + `--experimental-enable-authz`；`docs/authentication.md:28` 本身已过时 |
| 18 | HPA 示例存在 | ✅ 属实（仅 kind 验证；指标为 at_capacity worker 计数，多 Actor 下语义待重设计） | `demos/autoscaled-workerpool/` |
| 19 | （方案未提）Worker 崩溃→Actor CRASHED、无自动重调度 | 现状确认 | `workflow_worker_delete.go`、`worker_assignment_reconciler.go`；恢复靠 RevertActor |
| 20 | （方案未提）上传失败→CRASHED | 现状确认 | `cmd/atelet/main.go:747` TODO #362 |

### 4.2 AgentENV 侧

| # | 方案断言 | 结果 | 证据（AgentENV/ 下） |
|---|---|---|---|
| 1 | SandboxBackend 有 start/pause/resume/stop/snapshot/fork 边界 | ✅ 属实 | `src/sandbox/backend.rs:192-318`（trait 边界干净，但 Firecracker 实现内部调用 5 个进程级单例：Config/Ublk/Network/CustomExtension/Pool） |
| 2 | Factory 可不经 orchestrator 构造沙箱 | ✅ 属实 | `crates/benchmarks/benches/snapshot_benchmark.rs:128-157` 已实证；另有非 server 二进制先例 `src/bin/aenv-snapshot-image.rs`（"never starts node-runtime machinery"）——aenv-executor 可行性的直接证据 |
| 3 | manifest 路径 serde(skip)、序列化不足以搬运 | ✅ 属实 | `src/sandbox/manifest.rs:63-94` |
| 4 | UblkDeviceManager 共享表为进程内 | ✅ 属实（客户端侧）；daemon 侧已有 `active_shared` 引用计数表，跨进程原语部分存在 | `src/sandbox/ublk/device.rs:189-197`；`storage/ublk-daemon/src/server.rs:142` |
| 5 | daemon client 创建即 spawn、有 shutdown、无 connect-existing | ✅ 属实；补充：pidfd 父进程绑定 + CAP_SYS_ADMIN 降权 + 同 socket 二进程冲突 | `storage/ublk-daemon/src/client.rs:153-242,244-270,597-610`；`main.rs:341-378` |
| 6 | 网络槽必须全局领取、不可注入 | ✅ 属实 | `src/sandbox/network/manager.rs`（全局位图+暖池）、`sandbox.rs:1863-1866,2098-2101` |
| 7 | 调度策略忽略镜像 hint | ✅ 属实 | `services/scheduler/internal/strategy.go:18-49` |
| 8 | CPU 求交逻辑存在且必要 | ✅ 属实 | `services/scheduler/internal/cpu_template.go:45-77`；结果经心跳下发、节点写全局 RwLock |
| 9 | Gateway 调旧 Scheduler、转发旧 runtime、列表全节点扇出 | ✅ 属实 | `services/gateway/internal/server.go:262-272,234-249`、`cluster_list.go:189-198`（all-or-nothing） |
| 10 | 创建后从 HTTP 响应提取 ID 再 RecordAssignment | ✅ 属实 | `server.go:361-375,441-522`（ModifyResponse 钩子） |
| 11 | fork 一次调用同时捕获源+启动孩子 | ✅ 属实 | `sandbox.rs:448-504`：pause→resume→并发 build+start 子沙箱 |
| 12 | 快照捕获必须暂停源 VM | ✅ 属实（snapshot = pause→capture→resume，毫秒级暂停） | `sandbox.rs:383-406,1158-1184` |
| 13 | envd token 机制 | ✅ 属实，且重绑定实现比方案预期完整 | HMAC(seed, sandbox_id)、不序列化、恢复时重绑+MMDS 改写+重新 init（`sandbox.rs:724-755`、`envd.rs:161-207`） |
| 14 | orchestrator 自动恢复所有沙箱 | ❌ 不准：启动仅加载 Paused 元数据；恢复惰性；**停机 pause-all** | `orchestrator/service.rs:210,2784-2863`、`src/api/proxy.rs:721-736` |
| 15 | 预热池是跨沙箱进程级共享、仅恢复路径消费 | ✅ 属实 | `src/sandbox/firecracker/pool.rs:27`、`sandbox.rs:1975-1999` |
| 16 | 镜像缓存 immutable key、upper 每实例独立 | ✅ 属实 | `src/image/cache/graph.rs`（内容寻址 commits/<digest>）、daemon `materialize_overlaybd_runtime` |
| 17 | 内存恢复 = 共享只读 ublk + BackendType::File | ✅ 属实 | `device.rs:582-589`、`instance.rs:523-553`；脏页追踪依赖 **Firecracker fork 定制 API**（`instance.rs:464-485`） |
| 18 | Firecracker/overlaybd 为项目 fork | ✅ 属实 | `config/deps_manifest.toml`：firecracker `1.15.1-patch-v1`（kvcache-ai fork，含 dirty-memory-ranges 等）、overlaybd `v1.0.18-aenv.1`、guest kernel 6.1.175 |

**结论**：两份文档对两个代码库的理解真实深入——18+14 条断言中仅 3 条实质偏差（其中 2 条是基线漂移所致），且最难的几处判断（serde(skip)、daemon spawn、全局网络槽、fencing 缺失、Checkpoint teardown 契约）全部命中。这在评审中是高水位的表现。

---

## 5. 架构与实施计划评估

### 5.1 组件复杂度评估

| 新组件 | 必要性 | 复杂度/风险 | 备注 |
|---|---|---|---|
| ateom-agentenv（Go 适配器） | 必需 | 中 | 复用 internal/（ateomtunnel/ateomnet）受 internal 边界约束，须放在 substrate module 内 `cmd/ateom-agentenv/`——方案 §3.2 的处理正确 |
| aenv-executor（Rust） | 必需 | 中 | 有 benchmark 与 aenv-snapshot-image 两个先例；真正难点是 5 个全局单例的注入改造（方案 §5.2 ExecutionContext 设计正确） |
| aenv-storage-broker | 必需（单 Actor 路线）/缩水（多 Actor 路线） | **高**（设备 cgroup、账本、daemon 生命周期） | 见 3.2/3.5；是全方案最难的基础设施 |
| aenv-api-bridge | 必需（E2B 兼容目标） | 中 | 自有幂等/映射 schema 合理；授权层建议对齐 OpenFGA；需与 Substrate 状态做对账任务 |
| layer-catalog（M2） | 必需（M2 共享层） | **高**（跨 PG+对象存储的最终一致所有权/GC） | 方案 §8.3/8.4 的设计（owner/pin/pending_ops + 幂等补偿）方向正确，且诚实承认"钩子未完成则关 GC"；这是全方案正确性风险最高处 |
| cluster-extension（M3） | 可选 | 低-中 | 从 Scheduler 拆节点信息/CPU 求交/P2P 目录；注意 artifact 索引现状在 scheduler 内存且 HA 不覆盖（拆出反而是改进） |

6 个新组件 + S1-S9 补丁系列 + Gateway substrate 模式，整体规模是**多人、多季度项目**。M1（后端闭环）可独立交付价值（原生 API 驱动 AgentENV 后端）——分阶段策略正确。

### 5.2 里程碑与验收

- **M1**：范围现实。两个修正建议：①"两个兼容节点跨 Worker 恢复"依赖 P0-5 设备问题先行解决（建议 M1 内先单节点，跨节点恢复作为 M1.5）；②明确 EXTERNAL suspend 失败语义按现状（3.3）。
- **M2**：增加密度回归量化退出标准（3.2）；layer-catalog 钩子（S7）是硬前置。
- **M3**：fork/构建/卷的拆分合理；上游 roadmap 的 "Actor Forking/Cloning" 与 "Data locality in scheduling" 说明方向与上游一致，建议跟踪上游实现避免重复建设（S9 在线捕获若上游先做，直接复用）。
- **P0-P7 任务包**：拆分合理，但**没有任何工作量/人力/日历估计**——对一份用于"开发拆分与技术评审"的文档，这是最明显的缺口。建议每包给出人月级估计与依赖图，并明确 M1 最小团队配置。

### 5.3 测试计划

§17 的分层测试与 10 条故障注入清单直接命中真实薄弱点（幂等缝隙、晚到 Release、fencing、部分失败 fork），质量高。补充建议：
- 增加 K8S 层混沌：WorkerPool 滚动更新带活 Actor、节点 drain、Pod OOMKill（grace 3600s 内的 pause-all 是否完成）。
- 增加规模浸泡（100+ Actor 并发生命周期）与 PG 事务冲突率观测。
- 增加 header 协议契约测试（E2B SDK + aenv CLI 双客户端，见 3.8）。

---

## 6. 结论与建议行动清单

**总评：方案方向正确、事实功底扎实、风险意识良好，可以进入实施准备——但须先关闭以下事项。**

按优先级：

1. **【立即】重新基线化两份文档到 Substrate `756c2a537`**，修订 §3.1 所列的全部失效断言（actorsPerAteom、随机调度、RBAC、scope 语义、LOCAL/EXTERNAL 层次、DataOnGolden、BindActorToWorker 文件位置），并建立上游同步节奏（pre-1.0 + 227 提交/17 天的漂移速度）。
2. **【M2 前】完成"单 Actor vs 多 Actor Worker"ADR**，基于 #1836 的新事实重算 broker 必要性、密度经济学、HPA 语义三个变量；为 M2 增加密度/超分量化退出标准。
3. **【M1 前】明确 M1 失败语义边界**：EXTERNAL 上传失败 = CRASHED + RevertActor（现状）；Worker 崩溃 = CRASHED（无自动重调度）；把两者写入 §12.4 错误分类并定义 E2B 侧可见行为。
4. **【M1 前】P0 原型验证清单执行**（方案 §18 的清单本身是对的，按本评审补充的事实缩小范围）：动态 ublk 设备在非特权 Pod 的 device cgroup 放行（预分配池/CDI/DRA 三选一）、Firecracker fork 快照跨节点恢复（含路径/MAC/兼容域重写）、daemon pidfd/能力模型解耦、envd 跨节点 seed 共享。
5. **【设计补充】出站域名策略映射**（E2B allowOut/denyOut → Substrate egress）与 **E2B header 协议契约**（泛域名 + header 路由）两节。
6. **【设计补充】复用机制清单落地**：RequestActorSuspend（空闲回收）、request parking（await ready）、RevertActor（恢复路径）、UploadPausedCheckpoint（LOCAL→EXTERNAL 提升）、HardwareIdentity（CPU 兼容域挂点，替代纯标签方案）、OpenFGA（bridge 授权层对齐）。
7. **【工程管理】补工作量估计、人力配置、日历排期与依赖图**；建立与 Substrate 上游的协作策略（class 泛化、HardwareIdentity CPU 扩展等补丁争取上游合入，降低私有维护成本）。
8. **【文档质量】修复全部失效链接**（`../substrate/...`、`src/...`、`迁移到Substrate可行性分析.md`——实际文件名为 AgentEnv运行到Substrate可行性分析.md）；将 Firecracker/overlaybd fork 维护、GPU 范围外声明补入风险登记册；引用 `docs/threat-model.md` 做 bridge/broker 威胁建模。

---

## 附：评审局限性声明

本评审为静态分析：未编译、未部署、未运行任何集成或性能测试；对代理核实的关键发现做了抽样复核（基线 commit 对比、多 Actor 提交、lease 存在性、设备形态、OpenFGA、scope 枚举等），但未逐行复核全部 file:line 引用。Substrate 处于 pre-1.0 高速演化期，本评审的"当前 HEAD"结论（`756c2a537`，2026-10-07）同样会过时——这本身正是 P0-1 的结论。
