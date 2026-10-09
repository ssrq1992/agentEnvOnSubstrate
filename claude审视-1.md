# Claude 第二轮审视：对 Codex 回复与 v2 设计文档的复核

> 评审对象：[codex审视.md](codex审视.md)、修订后的 [可行性分析 v2](AgentEnv运行到Substrate可行性分析.md) 与 [软件实现设计 v2](Substrate_AgentENV软件实现设计.md)。
> 评审基线：与两份 v2 文档一致——Substrate v0.4.0（`756c2a53741121e728f4cc3066c8a19e575b4919`）、AgentENV v0.2.3（`6cccaa7842bd5be2051111d4f74d9e37aa721244`）。
> 评审方法：对 Codex 回复中全部可代码核实的新断言（B/R/N 共 14 条核心技术断言）做了独立源码核实——Substrate 侧 8 条、AgentENV 侧 5 条，由两个独立核实通道逐条给出 file:line 证据；本人另行复核了归属公允性、AGENTS.md pre-1.0 声明、RegisterWorker 注册流程、request parking 作用域四项。全部为静态分析，未编译/部署/实测。

---

## 1. 总体结论

**Codex 的回复是一份高质量的 rebuttal：对我第一轮评审的六条纠正（R1-R6）没有一条是稻草人——每一条都能在我第一轮原文中找到确切的过度表述原句；经代码核实，六条纠正全部实质成立，本评审全部接受。B1-B8、N1-N9 经逐条核实亦全部成立（其中 B2、N7、N9 需要精确化补充，见下文）。v2 文档声称的修订全部落实且与代码事实一致，未发现 v2 引入的新的事实错误。**

| 维度 | 判定 | 说明 |
|---|---|---|
| R1-R6（Codex 对我第一轮的纠正） | ✅ 全部成立，全部接受 | 逐条回查原文 + 代码核实；R3 的转述略强于我的原话，但纠正本身正确（§3） |
| B1-B8（部分接纳） | ✅ 全部成立 | 其中 B2、B5、B7 的代码证据比 Codex 表述更丰富（§4） |
| N1-N9（Codex 新发现） | ✅ 全部核实成立 | N9 的实际语义差距比 v2 §9.5 表所列更大（§5） |
| v2 文档修订落实度 | ✅ 完整 | A1-A10 声称的修订逐项在文档中找到；估算数字自洽（§6） |
| 双方共识的技术基线 | ✅ 已收敛 | 剩余分歧为零；开放项全部属于"待实测"类（§9） |
| 本轮新增发现 | 8 项双方文档均未覆盖的事实 | 多为精确性补充，2 项建议纳入设计（§7） |

**一句话结论：三轮评审（我 → Codex → 我）之后，技术分歧已经收敛到一致基线。v2 设计文档可以作为实施准备的基线；静态评审的边际收益已经递减，剩余风险必须由 P0 原型与故障注入实验关闭。**

---

## 2. 归属公允性核对（R1-R6 是否针对我真实的观点）

在接受或反驳纠正之前，先核对 Codex 是否公允地转述了我的观点。**结论：公允。** 六条纠正对应的原句均存在于我第一轮评审中：

| Codex 纠正 | 我第一轮的原句 | 公允性 |
|---|---|---|
| R1 多 Actor 超分 | §3.2 表："一个 Pod 内统计复用多 VM，最接近 standalone 的超分表现"；§6："密度经济学得以保全" | ✅ 属实（"最接近…超分表现"确系过度推断） |
| R2 SIGTERM 对齐 | §3.6.3："这个停机行为其实与 Substrate 的'SIGTERM 优雅 checkpoint + 3600s grace'路径天然对齐" | ✅ 属实（我对 Substrate 停机路径的表述有误） |
| R3 parking 覆盖 | §3.7 表：parking 是"bridge '创建 → Resume → await ready'…的直接支撑" | ⚠️ 转述略强（"自动覆盖" vs 我的"直接支撑"），但纠正方向正确 |
| R4 设备权限收窄 | §3.5："此问题大幅缩窄为'broker 自身 DaemonSet 的设备权限'"；"预分配设备池 / CDI / DRA 是仅有的正路" | ✅ 属实（前句在多 Actor 语境下自相矛盾，后句绝对化） |
| R5 预热池收益 | §3.2 表："池跨 Actor 共享，恢复路径收益完整保留" | ✅ 属实（未考虑池预绑定旧网络槽） |
| R6 经验数值 | §3.9.2："预计亚毫秒级、不构成风险" | ✅ 属实（未测量假设写成了判断） |

这不是姿态性核对——它决定了本轮评审的基调：**我对 R1-R6 的立场是接受，且逐条给出代码证据**。以下按条裁决。

---

## 3. 对 R1-R6 的逐条裁决

### R1（多 Actor 不自动获得统计超分）——接受，并补充一个双向边界事实

**接受纠正。** 核实确认完整链路：容量来自 Worker Pod 的 `limits.cpu/limits.memory`（DownwardAPI 投影成 `/run/ateom-capacity/` 文件，[register.go:40-64](substrate/internal/ateom/register.go)）→ 调度按 ActorTemplate limits 逐维累加预留（[worker_assignment.go:103-127](substrate/cmd/ateapi/internal/store/atepg/worker_assignment.go)，Worker 行锁内 admit 重查）→ `checkRoom` 校验 `sum(allocated+want) ≤ capacity`（[scheduling.go:252-282](substrate/cmd/ateapi/internal/scheduling/scheduling.go)）。共享 page cache 只降低物理用量，不改变逻辑容量；K8S 按 requests 放置 Pod 的区分也正确。我第一轮"最接近 standalone 超分表现"的表述确实不成立。

**补充事实（双向）**，供 v2 §1.2/§14.1 参照：

- **正向旁路**：不声明 limits 的 Actor **不做任何资源预留**——`admittedResources` 返回 nil 时（[workflow_resume.go:369-373](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)，"an actor declared no limits reserves nothing"），`checkRoom` 对未命名维度直接跳过，该 Actor 只受槽位约束（`--max-actors` 默认 1000）。即**当前系统里唯一存在的"超分"就是这条无治理旁路**。v2 §14.1 "不允许靠省略模板 limits 绕过容量模型"已正确封堵 Actor 侧。
- **反向陷阱**：Pod 容器**缺失** limits 时容量按 0 上报（[register.go:56-57](substrate/internal/ateom/register.go)："better to place nothing on a worker that cannot say what it has"）——带 limits 的 Actor 将无法调度到该 Worker。这是安全的默认，但意味着 agentenv WorkerPool 模板必须显式设置 limits，建议 v2 §14.1 把"缺失维度拒绝"显式扩展到 Worker 侧配置校验。

### R2（SIGTERM 不是自动快照）——接受，附两项对 v2 有利的新事实

**接受纠正。** 核实确认（[shutdown.go:68-121](substrate/cmd/ateom-microvm/shutdown.go) 及 gvisor 同构实现）：`gracefulShutdown` 只做四件事——置 draining 拒绝新 Run/Restore（`Unavailable`）、取消启动中操作、等**在途** RPC（含 checkpoint）排空、然后对每个 guest SIGTERM→等 30 分钟预算→SIGKILL。**不会为任何 Actor 主动发起快照或 suspend**。3600s 是 terminationGracePeriodSeconds 时间预算。我第一轮"SIGTERM 优雅 checkpoint…天然对齐"的表述错误——把"等在途 checkpoint 完成"误读成了"主动 checkpoint"。

但核实发现两项对 v2 有利的事实，建议写入 §15 drain 设计依据：

1. **CheckpointWorkload 在 draining 时刻意不被拒绝**（[ateom-gvisor/main.go:637-639](substrate/cmd/ateom-gvisor/main.go) 注释："Allow checkpointing even if the pod is shutting down. This will allow actors (or the harness) to suspend on shutdown."；microvm 同）。即控制面在 Pod 终止流程已启动后仍可驱动逐 Actor Suspend——v2 §15 安全 drain 的"控制面逐 Actor Suspend → 确认提交 → 退出 Pod"顺序在 3600s grace 内**可实施**，不是空想。
2. **`internal/ateomsuspend/` 是完整但未接线的库**：Requester（ateom→atelet→ateapi 的上行 suspend 请求，含 UID 防串、重试策略）无任何 ateom 二进制 import（仅自身测试引用）。v2 §10.2 让 adapter 接上它是正确设计，但应在文中注明"当前无调用方，触发链是新增工作"。

### R3（parking 只覆盖 router 入站路径）——接受

**接受纠正。** 核实确认 parking 实现位于 `cmd/atenet/internal/router/ingress/`（parking.go、resumer.go），是 router 对入站流量触发自动 Resume 的挂起重试机制；bridge 直连 ateapi 的 CreateActor/ResumeActor 不经过该路径。我第一轮"直接支撑"的表述把"数据面客户端重试"（成立）和"bridge 内部 await-ready"（不成立）混在了一起。v2 §10.2 的处理（bridge 自建持久幂等记录、有界退避、结果查询）正确，且"默认 5s 是 router 重试预算而非完成 SLA"的限定准确。

### R4（多 Actor 不消除设备访问要求）——接受

**接受纠正。** 我第一轮的两处表述均有问题：①"多 Actor 路线下此问题大幅缩窄为 broker 自身 DaemonSet 的设备权限"——多 Actor 路线下根本不需要 broker，且 Firecracker 进程仍在 Worker 容器内打开内存/rootfs ublk 设备（[sandbox.rs:2157](AgentENV/src/sandbox/firecracker/sandbox.rs)），per-Worker 的设备 cgroup/capability/io_uring 验证不可省略；②"预分配设备池 / CDI / DRA 是仅有的正路"——绝对化了，正确表述是"需在目标内核/CRI 上原型验证的候选机制，且不能据机制名称假定运行中新增 minor 已获放行"。v2 §1.2（"无论采用哪种形态…不能移除 device cgroup 校验"）与 §7.3 的处理准确。

### R5（预热池不能原样启用）——接受，补充两层暖池事实

**接受纠正，且代码证据比 Codex 表述更彻底。** 核实确认（[pool.rs:44-49](AgentENV/src/sandbox/firecracker/pool.rs)）：暖池条目 `WarmFirecracker { slot, fc_instance, work_dir }` 是**不可分割的归属单元**——填充时先 `NetworkManager::global().allocate_any()` 领全局槽，再把 Firecracker 进程 `setns` 进该槽的 netns（[instance.rs:120-130](AgentENV/src/sandbox/firecracker/instance.rs)，spawn 前执行，多线程进程事后无法迁移）。按 Actor 注入网络后失效的部分：槽的固定地址计划/netns/iptables/egress proxy、已在池 netns 内的进程、`host_interaction_ip` 寻址假设、`set_egress_policy` 的 netns 所有权假设、以及从全局 NetworkManager 取槽的填充路径。仍可复用的只有 work_dir + 空闲进程 + API socket，且前提是池先拿到该 Actor 的 netns 再 spawn。我第一轮"收益完整保留"不成立。

**补充**：① 消费路径仅限快照恢复（`start_resume`），fresh 启动从不用池（[sandbox.rs:1974-1976,1863-1919](AgentENV/src/sandbox/firecracker/sandbox.rs)）；② 实际有**两套**暖池——FirecrackerPool 之外 NetworkManager 自带暖 slot 池（[manager.rs:237-257](AgentENV/src/sandbox/network/manager.rs)，释放的 slot 连 netns/iptables 保温复用，可能残留上一租户 user 链规则），v2 §5.1 的池改造应同时覆盖两层；③ 池本身当前不涉及任何 cgroup 操作，v2 "领取时迁入正确 Actor cgroup" 是新增工作而非现有能力迁移——v2 的表述已是"重建/绑定"而非"迁移"，准确。

### R6（roadmap 与经验数值不是已确定结果）——接受

**接受纠正。** "预计亚毫秒级、不构成风险"确实写在我第一轮 §3.9.2——未测量假设不应以判断语气出现，正确写法是 v2 §17.2 的"不预估'亚毫秒'代替测量"。roadmap 方面：我第一轮用"runtime modularity 列为优先级 6"作方向佐证可以成立，但同时我写的是"争取上游合入"而非"必然接收"；v2 §16.2 的表述（"不构成上游会接收具体实现或日期的承诺"）是正确的收紧。此项双方实际分歧最小。

---

## 4. 对 B1-B8 的逐条裁决

| # | 裁决 | 核实结果与补充 |
|---|---|---|
| B1 单/多 Actor ADR | ✅ 成立，接受 | ADR-001（v2 §1.2）的论证链成立：跨 Worker 恢复只要求快照与 assets 可访问，不要求两 Worker 打开同一块设备——"单 Actor + Worker 内 daemon + 完整持久快照包"确实能完成 M1b 验收。我第一轮把 broker 必要性与单 Actor 路线绑定的表述被正确解开。ADR 对密度模型的保守处理（"按 limits 预留仍限制多 Actor，超分独立立项"）与 R1 核实一致 |
| B2 OpenFGA | ✅ 成立，附两项精确化 | 核实确认：注册表只登记 20 个 RPC（[registry.go:140-213](substrate/cmd/ateapi/internal/authz/registry.go)）——Atespace 4、AccessPolicy 7、ActorTemplate 4、Actor 5，全为 CRUD；Resume/Pause/Suspend/Revert/Tag/Worker/EgressPolicy/Mint* 均未登记；interceptor 对未登记 RPC 直接放行（[interceptor.go:33-36](substrate/cmd/ateapi/internal/authz/interceptor.go)）。**精确化 1**：受保护的不止 Actor/ActorTemplate（Atespace/AccessPolicy 也在），且 AccessPolicy 是 `alwaysEnforce`（开关关闭也强制）；v2 §9.2 的表述已准确，codex审视.md 的概括略窄。**精确化 2（重要）**：authn 是独立于 authz 的 interceptor（[apiauthn.go](substrate/cmd/ateapi/internal/apiauthn/apiauthn.go)，mTLS/Bearer JWT，无凭据拒绝），对所有 RPC 生效——authz 缺口的准确含义是"**已认证主体**可调用未登记的生命周期 RPC 而无 per-RPC 授权检查"，不是匿名可调。这一区分对 v2 §15.1 威胁模型的表述有直接影响（见 §8 建议 2） |
| B3 HardwareIdentity | ✅ 成立 | 第一轮已核实（architecture-only、`Matches` 无生产调用方）。v2 的分阶段处理（M1 可信池 + 执行端硬检查，S10 贯通）合理 |
| B4 上传失败分类 | ✅ 成立，证据精确 | 核实确认 [crash.go:60-81](substrate/cmd/ateapi/internal/controlapi/crash.go)：不 crash 的只有 Terminate RPC、工作流 ctx 结束、`Unavailable/Canceled/DeadlineExceeded` 三种传输码；上传失败在 atelet 内是普通 error，经 interceptor 转 `codes.Internal` → default 分支 → **CRASHED**（[main.go:755-758](substrate/cmd/atelet/main.go) TODO #362 原文确认）。v2 §6.2/§12.4 的分类表与代码逐项吻合 |
| B5 Worker epoch | ✅ 成立，附竞态窗口 | 核实确认：`Worker.epoch`（容器重启计数，RestartCount+1，[syncer.go:292-331](substrate/cmd/atecontroller/internal/workersync/syncer.go)）、`assignment.worker_epoch` 行锁盖章、reconciler 崩溃旧 epoch actor——DB 侧机制完整。同时确认执行 RPC 只带 `target_ateom_uid`（pod UID，容器重启不变），epoch **不在任何 RPC 中**；且 `validateAssignedWorker`（[workflow_resume.go:303-367](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)）不查 epoch，绑定与 epoch 抬升之间存在**竞态窗口**，目前靠 reconciler 事后兜底。建议 S5 范围内增加"绑定时校验 observed_epoch"（见 §8 建议 3） |
| B6 daemon 共享原语 | ✅ 成立 | 第一轮已核实 active_shared/pidfd；本轮 AgentENV 侧核实补充了会话/归属协议缺失的确认。v2 §7.1 的处理（WorkerLocal 复用共享表 + 补 digest/canonical path/会话账本）准确 |
| B7 token 稳定性 | ✅ 成立，证据充分 | 核实确认（[access.rs:71-84](AgentENV/src/sandbox/access.rs)）：token = HMAC-SHA256(seed, SandboxId)，确定性（同 seed 同 ID 必同 token，有单测固化 [access.rs:279-284](AgentENV/src/sandbox/access.rs)）；resume 用同一 sandbox_id 重新派生 → 客户端旧 token 仍有效；fork/新 ID 才换身份（[api/impls/sandbox.rs:210](AgentENV/src/api/impls/sandbox.rs) 生成 child_id）。此项同时澄清了我第一轮 §3.7"恢复时…换新身份"的不精确表述——准确说法是**身份字段可被覆盖，同 ID 恢复 token 不变，派生新 ID 才变**。另核实到两个 v2 应吸收的事实：① 快照内存确实残留旧 token，且 startup_pack 机制（[startup_pack.rs:95](AgentENV/src/sandbox/firecracker/startup_pack.rs)）**故意利用**残留 token 让录制 VM 连上恢复出的 envd——设计者明知该事实，v2 §5.3 "init 完成前阻断流量"的要求有据；② 跨节点 seed 一致性已有显式警告（[access.rs:61-66](AgentENV/src/sandbox/access.rs)：不统一配置 `AENV_SANDBOX_ACCESS_TOKEN_HASH_SEED` 则跨节点恢复校验失败）——v2 §5.3 TokenProvider 的跨节点实测项应以此为已知风险 |
| B8 M1a/M1b 与估算 | ✅ 成立 | 数字核对自洽：P0-P4 = 3-5+3-4+5-8+8-14+6-10 = **25-41 人周** ✓；P5/P6 = 6-10+10-18 = **16-28 人周** ✓；三人投入下 12-20 日历周含 30% 缓冲与串行依赖，量级合理。M1a/M1b 拆分把"跨 Worker 恢复不依赖跨 Pod 设备共享"的依赖解开（与 B1 一致），且未降低 M1 最终门槛（两节点仍是退出条件） |

---

## 5. 对 N1-N9 的核实结果

全部成立。逐条：

| # | 核实 | 关键证据与补充 |
|---|---|---|
| N1 数据面授权执行点丢失 | ✅ 成立 | [auth.rs:43-135](AgentENV/src/api/impls/auth.rs) `require_auth` 是全 app middleware，执行序在自动唤醒**之前**；envd 端口按 `secure` + `x-access-token` HMAC 校验、非 envd 端口按 `allowPublicTraffic` 或 `e2b-traffic-access-token`；转发前剥离 `x-api-key`/`x-access-token`/traffic token/路由头。Go Gateway 对数据面**显式不做认证**（[server.go:988-1005](AgentENV/services/gateway/internal/server.go)："enforced by the owning runtime node"）——**节点 runtime HTTP 是数据面授权的唯一执行点**，N1 完全成立。丢失后的五类暴露（私有端口裸奔、secure envd 失门槛、template_builder 不再 404 隐藏、凭据泄入 guest、未授权唤醒 DoS）均确认。v2 §9.4 的迁移清单与检查点一一对应 |
| N2 模板替换触发隐式 DATA | ✅ 成立 | [workflow_resume.go:184-186](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)：外部快照记录的模板 UID ≠ 当前模板 → `TemplateReplaced=true` → scope 强制 `SNAPSHOT_SCOPE_DATA`（:683-686）→ **冷启动** + 从快照 untar 卷（[restore.go:179-190](substrate/cmd/ateom-microvm/restore.go)："A Data snapshot holds no guest state, so this is a cold boot"），内存状态丢弃。UpdateActor 允许换模板但仅限 SUSPENDED 且要求 SandboxConfig `proto.Equal`、卷一致、快照位置不变。v2 §1.1 "M1 必须拒绝此模板替换/恢复组合"是正确的防御（对 AgentENV 后端而言，DATA 恢复语义等价于"丢内存重建"，不应静默发生） |
| N3 LOCAL pause 会话释放 | ✅ 成立 | 第一轮已核实 pause 释放 Assignment（ReleaseActorFromWorker）。绑定运行 session 的 pin 会随之消失，LOCAL snapshot 必须独立 owner。v2 §8.3/§8.4 设计正确 |
| N4 Revert 无恢复点保证 | ✅ 成立 | [workflow_revert.go:245-253](substrate/cmd/ateapi/internal/controlapi/workflow_revert.go)：Revert 落 SUSPENDED、清 LocalSnapshot/InProgress/Crash，保留 ExternalSnapshot；Resume 三分支中无外部快照即 "Booting from ActorTemplate spec" 冷启动（[workflow_resume.go:712-714](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)）。**从未快照过的 CRASHED actor，Revert+Resume 就是全新重建**。附带发现：Revert 不清理节点上的 local checkpoint 文件（revert.go:90-91 TODO #641）——孤儿快照累积，v2 §15 巡检工具应覆盖（见 §8 建议 4） |
| N5 RESOURCE_EXHAUSTED → CRASHED | ✅ 成立 | crash.go 仅豁免三种传输码，`ResourceExhausted` 落 default → CRASHED。v2 §4.3/§12.4 要求 S5 显式实现 NO_EFFECT 回滚 + 有界重选，正确且必要 |
| N6 pre-1.0 无兼容承诺 | ✅ 成立（本人核实） | [AGENTS.md:69](substrate/AGENTS.md) 原文："Agent Substrate makes no compatibility guarantee before v1.0.0. Do not add code to stay compatible with older clients, binaries, protos, flags, or config." v2 §14.2 的成套锁定 + 隔离安装/协调升级策略是对该约束的正确回应 |
| N7 per-actor cgroup 仅 cpu.max | ✅ 成立，附 gvisor 对照 | [actor.go:47-69](substrate/internal/ateomcgroup/actor.go)：`OpenActorLeaf` 唯一写入是 `cpu.max`（未设限写 `max 100000`），包内无任何 memory.max/memory.high 写入；microvm 的内存上限只有 guest RAM 尺寸 + Pod 级 cgroup。**对照**：gvisor 路径经 `ApplyToOCISpec` 把 CPU quota + memory limit 写进 OCI spec、由 runsc 施加到 sandbox 叶（[sizing.go:91-103](substrate/internal/sizing/sizing.go)）——即 Substrate **已有 per-actor 内存上限的先例模式**，AgentENV 后端需把 memory.max 引入 ActorLeaf 或等效机制，是"有先例的新工作"而非无解。v2 §15 的表述准确 |
| N8 CPU 配置非全局单例 | ✅ 成立 | [server.rs:112-128](AgentENV/src/bin/server.rs)：`Arc<RwLock<Option<String>>>` 在 server 构造，注入 TemplateBuilder + FirecrackerSandboxFactory；消费方只有 factory 的 **fresh 启动**（build()）与 TemplateBuilder；`build_from_snapshot`/`build_from_paused_state` 不读。embedded 固定值注入可行，无隐藏消费者；且快照恢复**有意**不重放 cpu-config（config.rs:526-530 注释：CPU 状态已在 vm_state.bin 内，重放会被 Firecracker 拒绝）——"心跳变化不影响旧快照恢复"有代码级保证，v2 §5.2 的设计据此成立 |
| N9 E2B 策略语义不等价 | ✅ 成立，差距比 v2 表更大 | AgentENV 侧全部证实：allowOut 域名/IP/CIDR（[policy.rs:47-59](AgentENV/src/sandbox/network/policy.rs)）、denyOut 仅 IP/CIDR（域名直接报错）、显式 allow 覆盖用户 deny（allow ACCEPT 规则排在 deny REJECT 前）、节点级绝对禁区（10.0.0.0/8 等）优先于一切。Substrate 侧：**纯 allow 集合无 deny**（空策略=全拒，与 E2B Default=默认放行**姿态相反**）、**无 CIDR**（IP 字面量仅被 `*` 匹配）、仅 HTTP(S)/TLS passthrough（UDP 除 DNS 全断、其他 TCP 全断）、`*.example.com` 只匹配单级子域（AgentENV 匹配多级）、https=MITM（AgentENV 是透明 REDIRECT+首包嗅探，无需 CA 信任）。v2 §9.5 的"不接受/子集/UNSUPPORTED"三分处理正确；补充一个 v2 应显式列入的**最常见不兼容形态**：AgentENV 域名 allow 强制要求 `denyOut=["0.0.0.0/0"]`（[api/impls/sandbox.rs:644-660](AgentENV/src/api/impls/sandbox.rs)，否则创建直接 400）——迁移时绝大多数现存策略会落进 UNSUPPORTED 分支 |

---

## 6. v2 文档落实核查

对 Codex 声称的 A1-A10 修订逐项核查，**全部落实且与代码一致**：

| 声称 | 落实位置 | 核查 |
|---|---|---|
| A1 基线固定 | 两文档头部 + §20 | ✅ tag/SHA 一致，227/9 提交计数一致 |
| A2 调度更新 | §4.3、§11.1 | ✅ power-of-two-choices + 主导利用率比较 + 缓存缺失回退，与 scheduling.go 一致 |
| A3 术语修正 | §1.1 | ✅ DataOnGolden 已移除、on_pause 不存在已注明、LOCAL/EXTERNAL 层次归正（S6 改为"按需新增"） |
| A4 resume-all 修正 | §5.1 | ✅ 加载 paused 元数据/惰性恢复/停机 pause-all，与 R2 修正后的边界一致 |
| A5 复用机制 | §10.2、§12.3 | ✅ RequestActorSuspend/UploadPausedCheckpoint 分工明确（另见 §8 建议 1 的"未接线"注记） |
| A6 E2B 契约 | §9.4、§9.5 | ✅ header 优先级/泛域名/%2F/入站授权/出站子集全部成文 |
| A7 fork/威胁建模 | §15.1 | ✅ 风险登记表含 fork 版本链与信任边界清单 |
| A8 GPU/UDS/密度 | §1.1、§17.2 | ✅ GPU 列首期不支持；UDS "不预估亚毫秒代替测量"；密度指标 D/reuse 已定义 |
| A9 混沌/浸泡 | §17.1 | ✅ 16 条故障注入（原 10 条基础上扩），含 OOMKill/drain/100+ Actor 浸泡/authz 开关/多 Actor 隔离 |
| A10 工程计划 | §14、§16.1/16.2 | ✅ 人周/人员/依赖图/日历区间/上游策略/成套升级边界齐全 |

**内部一致性**：估算数字求和自洽（25-41 / 16-28，见 B8）；M1a/M1b 在 §1.1、§6、§16.1 依赖图、§17.3 门槛四处表述一致；§20 裁决表与正文 §1-§19 的修订一一对应。**未发现 v2 引入的新事实错误。**

v2 可行性分析（总体方案侧）同步核查：多 Actor 事实、OpenFGA 边界、power-of-two、M1a/M1b、"多 Actor 不等于内存超分"、drain 流程、本次复核新增边界一节——与实现设计 v2 及代码事实一致。

---

## 7. 本轮核实新增的事实补充（两份 v2 文档均未覆盖）

以下为两个核实通道独立发现的、**Codex 与我第一轮都没有写入**的事实。按对设计的影响排序：

1. **authn/authz 分层**：所有 Control RPC 先过 apiauthn（mTLS/Bearer JWT，无凭据拒绝）再到 authz。authz 缺口 = 已认证主体的越权，不是匿名可调。worker-facing RPC（RegisterWorker 等）由 atelet SPIFFE 认证保护（workerservice/register.go:37 有 "TODO(identity): This check should be handled by OpenFGA" 注释，佐证上游也认为该层待补）。
2. **CheckpointWorkload 在 drain 时刻意放行**（代码注释明言供停机时 suspend）——v2 §15 安全 drain 的可实施性依据，值得显式引用。
3. **ateomsuspend.Requester 无调用方**（完整库、零引用）——v2 §10.2 的 idle suspend 触发链是新增接线工作，不是既有行为。
4. **绑定竞态窗口**：`validateAssignedWorker` 不查 epoch，epoch 抬升与绑定提交之间存在窗口，靠 reconciler 事后兜底（崩溃而非拒绝）。
5. **无 limits 的 Actor 不预留任何资源**（仅受槽位约束）——当前唯一的"超分"是无治理旁路；同时 Worker 容器缺 limits → 容量 0 → 有 limits 的 Actor 调度不进。两个方向都应在 agentenv 准入中显式封堵。
6. **Revert 遗留 local checkpoint 孤儿**（TODO #641，只清 DB 指针不删文件）。
7. **两套暖池**：FirecrackerPool 之外 NetworkManager 自带暖 slot 池（netns/iptables 保温，可能残留上一租户 user 链规则）——v2 §5.1 池改造需同时覆盖。
8. **startup_pack 故意利用快照内存残留 token**——证明"内存快照含旧 token"是设计者已知并利用的事实，v2 §5.3 的"init 前阻断流量"要求必要性获得直接代码佐证；跨节点 seed 不一致的失败模式在 access.rs:61-66 已有显式警告。

另有两处上游过时注释（不影响结论，供后续 rebase 清理）：crash.go:51-53（说错误码多为 Unknown，实际统一转 Internal）、syncer.go:352-355（说 CreateWorker 限单 Actor，实际不设容量）。

---

## 8. v2 遗留问题与精确化建议

均为精确性补强，无阻断项：

1. **§10.2**（idle suspend）：注明 `ateomsuspend.Requester` 当前无调用方，触发链为新增工作；同时可引用"CheckpointWorkload 在 drain 中放行"作为 drain 顺序的可行性依据。
2. **§9.2/§15.1**（威胁模型）：补 authn/authz 分层事实——把"未登记 RPC 无授权检查"的暴露准确定性为"已认证主体的越权"，避免威胁建模按"匿名可调"过度设防或按"已有认证"漏防越权。
3. **§12.5/S5**（fencing）：增加"绑定时校验 observed_epoch"以关闭绑定竞态窗口——这属于 S5 已列工作范围内的一个具体校验点，成本极低。
4. **§15**（巡检）：巡检工具清单加入"Revert 遗留的 local checkpoint 孤儿"（TODO #641）。
5. **§9.5**（E2B 出站）：把"域名 allow 强制 denyOut=[0.0.0.0/0]"列为 UNSUPPORTED 分支的最常见触发形态；显式写明两侧**默认姿态相反**（E2B Default≈放行 vs Substrate 空策略=全拒）——这两点决定迁移存量策略时绝大多数输入会落入拒绝分支，影响 M2 兼容子集的实际覆盖率预估。
6. **§5.1**（预热池）：提及 NetworkManager 暖 slot 池这第二层，避免实现时只改 FirecrackerPool。
7. **§14.1**（资源配置）：把"缺失资源维度拒绝准入"显式扩展到 Worker 侧（Pod limits 缺失 → 容量 0 的隐式失效模式），并注明"无 limits 模板 = 零预留"旁路已在 Actor 侧封堵。
8. **§5.3**（token）：跨节点 seed 一致性风险可引用 access.rs:61-66 的既有警告与 startup_pack 的反例利用，作为 P4 实测项的已知输入。

---

## 9. 结论与下一步

**三轮评审后双方立场收敛情况：**

- 我第一轮的 8 项行动建议：6 项被完整采纳落实（重新基线化、ADR、失败语义、复用机制、工程计划、文档质量），2 项被正确收窄后落实（设备/跨节点/daemon/envd 原型→按 WorkerLocal/NodeBroker 分形并保留验证要求；出站映射→M2 子集 + 明确拒绝）。
- Codex 对我的 6 条纠正：全部接受（本评审 §3），其中 R2/R5 我原表述的错误程度经核实比 Codex 描述的还深（SIGTERM 根本不发起 checkpoint；暖池槽与进程是 setns 级永久绑定）。
- Codex 的 9 条新发现：全部经代码核实成立。
- 本轮我新增 8 项事实补充：方向上均**强化**而非动摇 v2 的设计决策。

**静态评审到此为止是合理的**——剩余的全部开放项（设备 cgroup 放行、跨节点恢复、真实共享率与密度、token 跨节点与泄露窗口、UDS 开销）都只能由 v2 §17/§18 已列的实测关闭，再审一轮文档不会产生新信息。

**建议的下一步**（按序）：

1. 按 §8 的 8 条建议出一版 v2.1 文档补丁（均为精确性修订，半天工作量）。
2. 启动 P0：环境基线 + 设备原型（动态 ublk 在目标 CRI/内核的放行验证）+ 两节点快照恢复原型——这三项是 v2 所有后续阶段的共同前置，且全部属于"静态分析无法回答"类。
3. P0 期间建立上游同步节奏（v2 §16.2 已定义策略）：按 227 提交/17 天的漂移速度，P0 结束时需要刷新一轮 §20 的证据表。
4. M2 动工前冻结 §17.2 的 D/reuse 密度门槛数值——v2 已把该项列为"P0 产出"，保持该承诺。

---

## 附：评审局限性声明

本评审为静态分析：未编译、未部署、未运行任何集成或性能测试。核实工作由两个独立通道完成（Substrate 侧 8 项断言、AgentENV 侧 5 项断言，合计 91+104 次源码检索/阅读），本人对其关键结论做了抽样复核（AGENTS.md、RegisterWorker/parking 作用域、R1-R6 归属原句），但未逐行复核全部 file:line 引用。Substrate 处于 pre-1.0 高速演化期（第一轮评审的核心结论之一），本评审基于的 v0.4.0 同样会过时——P0 阶段应按 v2 §16.2 的节奏刷新证据。
