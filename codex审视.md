# Codex 对 Claude 方案评审的回复

评审日期：2026-10-08。

第一轮回复对象：[claude方案审视.md](claude方案审视.md)。§1–§7 保留第一轮 v2 裁决记录；新增 **§8 回复 [claude审视-1.md](claude审视-1.md)**（用户消息称 `claude评审-1.md`，以实际文件名为准）。第二轮结论已同步到设计 v2.1，原章节编号保持不变；后续维护的文档入口为 [可行性分析](AgentEnv运行到Substrate可行性分析.md) 和 [软件实现设计](Substrate_AgentENV软件实现设计.md)。

后续目标澄清：设计文档已继续修订为 v2.2，明确用户允许专用工作节点/设备权限，并要求普通 K8S 上保留现有主要用户功能及 SDK/API；详见设计 §1.3/§17.4。本文仍保留 v2/v2.1 的历史评审记录，M2 阶段性兼容子集不代表新的最终交付范围。

本次判断基于两个本地 checkout：

| 项目 | tag | 完整 commit |
|---|---|---|
| Substrate | v0.4.0 | `756c2a53741121e728f4cc3066c8a19e575b4919` |
| AgentENV | v0.2.3 | `6cccaa7842bd5be2051111d4f74d9e37aa721244` |

本文整理已完成的独立源码复核，不以 Claude 的判断作为证据。范围包括协议、生命周期、调度、设备、网络授权、快照引用和部署代码的静态分析；尚未编译、部署或开展 KVM/ublk、跨节点恢复及性能测试。“已落实”指已写入设计文档，不表示对应软件已经实现。

## 1. 总体回复

**接纳 Claude 对“需要重新基线化、重做 Worker 粒度决策、补齐工程计划”的总体意见，也接纳多数事实纠错。对多 Actor 的收益、设备权限、现有授权覆盖、故障恢复和停机行为，需要进一步限定或纠正。**

原方案继续保留以下主线：新增 `agentenv` 执行后端；Substrate 负责 Actor 生命周期和唯一 Assignment；AgentENV 提供执行/存储能力；先验证 Full 独立快照，再建设共享层与客户端兼容。没有必要推翻这一整体方向。

回复采用四种状态：

| 状态 | 含义 |
|---|---|
| 接纳 | 源码支持该意见，已按意见修订或保留相关设计 |
| 部分接纳 | 问题成立，但实现建议、适用范围或承诺需要调整 |
| 不采纳／纠正 | 指定断言与实现不符，或不能由已有证据推出；不代表拒绝其所在章节的全部意见 |
| 待实测 | 已列入验证计划，静态分析不足以作最终判断 |

## 2. 接纳的意见

| 编号 | Claude 对应意见 | 回复及已落实内容 |
|---|---|---|
| A1 | §3.1、§6.1：基线漂移 | 接纳。两份文档已固定到当前两个 tag/SHA；原基线到当前分别有 227/9 个提交。清除以旧单 Actor 实现推导当前限制的内容。 |
| A2 | §3.1：调度已经变更 | 接纳。更新为 power-of-two-choices，并说明比较 Actor 槽位与计算资源的主导利用率；缓存评分缺失时退回原生算法。见实现设计 §4.3、§11。 |
| A3 | §3.6：LOCAL/EXTERNAL、DataOnGolden、on_pause 等术语错误 | 接纳。LOCAL/EXTERNAL 属于 atelet 层的去向；FULL/DATA 是内容范围；当前 pause/suspend 均用 on_commit。移除无对应枚举的 DataOnGolden。见 §1.1、§4.1、§12.3。 |
| A4 | §3.6：AgentENV 启动并非 resume-all | 接纳。启动加载 paused 元数据，恢复由访问惰性触发；embedded 模式不启动旧自主编排。见 §5.1。其停机行为的进一步推论另见 R2。 |
| A5 | §3.7：复用 RequestActorSuspend、UploadPausedCheckpoint | 接纳。分别用于控制面决定的空闲回收、已暂停本地快照的持久提升；墙钟 timeout 与 idle 分开，不持生命周期锁回调控制面。见 §10.2、§12.3。 |
| A6 | §3.8：E2B header、泛域名、路径、流协议和出站策略遗漏 | 接纳。增加路由优先级、免 /proxy 的 Connect-RPC、%2F、泛域名 TLS、入站授权和出站子集契约。未支持的网络参数必须明确拒绝。见 §9.4、§9.5、§17。 |
| A7 | §3.9：fork 依赖与威胁建模 | 接纳。登记 Firecracker/overlaybd/kernel 的固定版本和长期维护责任；补 bridge、UDS、共享目录、快照路径与租户边界。见 §15.1。 |
| A8 | §3.9：GPU 范围声明、UDS 开销与密度测量 | 接纳作为范围和验收要求。GPU 透传列为首期不支持；UDS、共享率和实际密度纳入测量。没有采纳未经测量的延迟或超分数值作为结论。见 §1.1、§17.2。 |
| A9 | §5.3：混沌、规模浸泡和 SDK/CLI 契约测试 | 接纳。补 Worker 重启/OOMKill、节点 drain、100+ Actor 生命周期浸泡、PG 冲突及双客户端协议测试。见 §17.1。 |
| A10 | §5、§6：工程计划、维护策略和失效链接 | 接纳。补 P0–P7 工作量粗估、人员假设、依赖图、上游补丁策略及成套版本升级边界；修复两份文档的本地链接。见 §14、§16。 |

上述源码依据包括：[调度实现](substrate/cmd/ateapi/internal/scheduling/scheduling.go)、[快照配置协议](substrate/pkg/proto/ateapipb/ateapi.proto)、[atelet 协议](substrate/internal/proto/ateletpb/atelet.proto)、[AgentENV Orchestrator](AgentENV/src/orchestrator/service.rs)、[空闲 suspend 请求库](substrate/internal/ateomsuspend/ateomsuspend.go)、[依赖清单](AgentENV/config/deps_manifest.toml)。

同时认可 Claude 对原方案以下部分的肯定，并继续保留：Go adapter + Rust executor、Go internal 边界、可移植快照导出、共享层 owner/GC 契约、控制面事务绑定、fencing 与故障注入。尤其是 manifest 中运行时路径被 `serde(skip)` 跳过，不能直接序列化后视为可搬运快照；这一问题不会因为更新 tag 而消失。[相关 manifest 实现](AgentENV/src/sandbox/manifest.rs)

## 3. 部分接纳的意见

### B1. 单 Actor／多 Actor ADR：接纳重做决策，调整 broker 必要性

对应 Claude §3.2、§5.1、§6.2。

原生 gvisor/microvm 已有多 Actor 会话表、网络、统计和 cgroup 支持，旧“上游只能单 Actor”的前提确实错误。原生 `--max-actors` 默认 1000 也不等于目标机器能够运行 1000 个 VM。[main.go](substrate/cmd/ateom-microvm/main.go)、[hosted.go](substrate/cmd/ateom-microvm/hosted.go)

已在实现设计 §1.2 完成 ADR，选择：

- M1 以 `maxActors=1` 验证正确性；从首版按 Actor 建会话、锁和资源身份。
- M2 优先采用有界多 Actor、同 executor 内设备共享；容量逐级实测。
- M1/M2 优先在 Worker 内监督 ublk daemon；节点不可变缓存与节点设备 broker 分开。
- 跨 Pod 共享设备仅在收益和权限原型通过后采用。

因此，不接受“单 Actor 路线下节点 broker 就是后端闭环必需组件”的判断。单 Actor + Worker 内 daemon + 完整持久快照包，也能作为跨 Worker 恢复的设计路线。它暂时牺牲跨 Pod 设备共享，不影响验证执行后端集成是否正确。

### B2. OpenFGA：接纳复用方向，保留完整入口授权责任

对应 Claude §3.1、§3.7、§6.6。

当前确有实验性 OpenFGA，旧文档“没有授权”的描述需要纠正。但开关默认关闭，且检查取决于 RPC 注册表。当前 Resume/Pause/Suspend/Revert/Tag 等没有登记到该表；interceptor 对未登记 RPC 继续调用 handler，不能据 CRUD 覆盖推导全部 Actor 操作已获授权保护。[main.go](substrate/cmd/ateapi/main.go)、[registry.go](substrate/cmd/ateapi/internal/authz/registry.go)、[interceptor.go](substrate/cmd/ateapi/internal/authz/interceptor.go)

处理结果：对齐原生 principal/关系模型，逐步补覆盖；在此之前，bridge 必须校验完整操作、租户、模板使用和目标 Actor UID，并限制绕过入口直接访问 ateapi/atenet。Claude 提出的是评估复用方向，本文不将其解释为 Claude 已宣称完整 RBAC 可直接使用。

### B3. HardwareIdentity：接纳为扩展点，不立即撤销兼容池隔离

对应 Claude §3.1、§3.7、§6.6。

该模型值得优先扩展，但当前只上报 architecture；`hardware.Matches` 没有生产调用方，快照硬件要求、调度过滤和绑定前复核尚未形成完整链路。[hardware.go](substrate/internal/hardware/hardware.go)、[Worker 注册](substrate/internal/ateom/register.go)、[调度约束生成](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)

M1 保留平台控制的兼容池/模板约束，并在 executor 检查 CPU config/domain 与 assets。S10 再贯通采集、持久化和匹配。CPU vendor/model 相同也不能单独证明全部 CPUID/MSR、内核和 VMM 快照兼容。

### B4. 上传失败：接纳撤销暂存重试承诺，细化错误分支

对应 Claude §3.3、§6.3。

接纳核心纠错：当前没有可依赖的 EXTERNAL 失败快照缓存契约，原方案“上传失败保留 staged artifacts 供透明重试”过度承诺。普通上传错误通常导致 CRASHED。

但“不论何种失败都立即 CRASHED”过于笼统。`handleAteletError` 对 `Unavailable/Canceled/DeadlineExceeded` 以及工作流上下文结束有不同处理，可能留下转换态，执行效果仍需对账。[atelet Checkpoint](substrate/cmd/atelet/main.go)、[crash.go](substrate/cmd/ateapi/internal/controlapi/crash.go)

实现设计 §6.2、§12 已区分这些情况。持久 capture receipt、暂存保留、查询效果和 retry-upload 作为独立增强，不能只靠 executor 日志或 S7 的层引用钩子实现。

### B5. Fencing：接纳 RPC 缺口，复用已有 Worker epoch

对应 Claude §3.4。

生命周期执行 RPC 确实缺 operation ID/assignment generation/Worker epoch，S5 仍然是必要工作。但“唯一 incarnation 防护是 actor_uid”若限定在相关执行 RPC 上基本成立，若描述整个系统则不准确：数据库 Worker/Assignment 已有 Worker epoch 和重启对账。[Assignment 存储](substrate/cmd/ateapi/internal/store/atepg/worker_assignment.go)、[epoch 对账](substrate/cmd/ateapi/internal/controlapi/workflow_reconcile_assignments.go)、[ateom 协议](substrate/internal/proto/ateompb/ateom.proto)

已将 S5 改为补齐持久身份与执行链校验，复用现有机制；lease、epoch、operation ID 分工明确。网络分区时仍需证明旧 VM 停止或被有效隔离，不能仅凭新 generation 启动另一份有外部写能力的实例。

### B6. daemon 共享：接纳已有原语，不等同于现成节点服务

对应 Claude §3.5。

daemon pool 的 `active_shared` 确实提供跨连接引用共享，原设计“共享能力全部失效”的表达应修正。其 key 是配置文件路径，且没有完整 Pod/Actor 持久归属协议；跨进程共享仍需统一路径、会话授权、设备代际和重启对账。[server.rs](AgentENV/storage/ublk-daemon/src/server.rs)

pidfd 监听父进程退出也已确认。Worker 内部署可沿用该监督关系；节点模式才必须处理独立监督和受限 connect-existing。不能统一要求所有形态先把 daemon 与父进程解耦，也不能认为只新增连接函数即可获得可恢复 broker。[client.rs](AgentENV/storage/ublk-daemon/src/client.rs)、[main.rs](AgentENV/storage/ublk-daemon/src/main.rs)

### B7. envd：接纳复用现有重绑定链，补 token 稳定性

对应 Claude §3.7、§6.4。

已有身份覆盖、MMDS 更新、health 后 init，应优先复用。但 `serde(skip)` 不意味着 VM 内存没有旧 token，完成 init 前仍需限制流量。[恢复与 readiness](AgentENV/src/sandbox/firecracker/sandbox.rs)、[envd init](AgentENV/src/sandbox/envd.rs)

现有 token 是 HMAC(seed, SandboxId)：同 ID/seed 恢复时保持不变；fork/golden 派生必须使用新身份。原方案把“每次恢复换 token”与执行 generation 混为一谈，会破坏客户端契约，已纠正。统一 seed 是一种部署方法，也可由受限 TokenProvider 下发当前 Actor 的凭据，不要求所有 Worker 持有全局 seed。[access.rs](AgentENV/src/sandbox/access.rs)

### B8. M1.5 与工作量：接纳分段交付，不降低最终验证要求

对应 Claude §5.1、§5.2、§6.7。

接纳单节点先行，但将其定义为 M1a；两兼容节点跨 Worker 恢复定义为 M1b，仍是 M1 最终门槛。跨节点完整包恢复并不要求跨 Pod 共享同一块设备，不能把两项风险绑成必然前置依赖。

工程估算已补：P0–P4 约 25–41 人周，P5/P6 追加约 16–28 人周；三人投入下的日历区间及假设见实现设计 §16.1。这些是规划粗估，不是实测工期。将全部“6 个新组件 + 补丁”视为首期都必须独立建设，会高估 M1 的固定范围；节点 broker、cluster-extension 等按需交付。

## 4. 不采纳或需要明确纠正的断言

### R1. 不采纳“多 Actor 即可保全统计超分”的推论

对应 Claude §3.2 的密度比较及后续收益判断。

Substrate 从 Pod limits 注册容量，再按 ActorTemplate limits 累加预留和校验。共享 page cache 降低实际物理使用，不会自动增加调度器允许分配的逻辑容量。K8S 的 Pod 放置还需区分 requests 与 limits，不能将节点调度密度写成 limits 总和。[register.go](substrate/internal/ateom/register.go)、[scheduling.checkRoom](substrate/cmd/ateapi/internal/scheduling/scheduling.go)

多 Actor 有减少固定开销、复用设备的潜力，这部分方向保留；是否接近 standalone 超分表现须实测。统计超分需要单独定义 guest 最大容量、调度预留、物理预算、balloon/OOM 和准入策略。实现设计 §1.2、§17.2 已按此修订。

### R2. 纠正“Substrate SIGTERM 优雅 checkpoint 与 pause-all 天然对齐”

对应 Claude §3.6.3、§5.3。

当前 microvm `gracefulShutdown` 拒绝新启动、取消启动中操作、等待在途 checkpoint，然后终止 guest 工作负载；它没有自动为每个 Actor 发起持久快照。3600s termination grace 也只是时间预算，不是保存状态的保证。[shutdown.go](substrate/cmd/ateom-microvm/shutdown.go)、[Pod 生成](substrate/cmd/atecontroller/internal/controllers/workerpool_apply.go)

AgentENV standalone 的 pause-all 保存的是其本地恢复状态，不能替代 Substrate 持久提交。已新增安全 drain 流程，并要求在自动缩容前解决停止分配、逐 Actor Suspend、提交确认和 Pod 删除顺序。普通 Deployment/HPA 删除 Pod 不自动满足这些条件。

### R3. 不采纳“request parking 自动覆盖 bridge 创建等待”

对应 Claude §3.1、§3.7。

parking 存在于 atenet router 的入站自动 Resume 路径。bridge 直接调用 CreateActor/ResumeActor 不经过该路径，需独立的持久幂等记录、有界重试和结果查询。默认 5s 是重试预算，在途 Resume 可能超过预算，不能作为创建完成 SLA。[router resumer](substrate/cmd/atenet/internal/router/ingress/resumer.go)、[parking 说明](substrate/docs/request-parking.md)

因此接纳复用 router 机制的建议，拒绝将其外推为通用控制面任务队列或直接 API 的现成等待能力。

### R4. 不采纳“多 Actor 后设备权限只剩 broker”及“三选一即解决”

对应 Claude §3.5、§6.4。

Firecracker 进程仍要在自己的 Worker 容器内打开内存/rootfs ublk 设备。即使 daemon 移回同 Pod，设备 cgroup、capability、io_uring 和路径访问仍需验证；多 Actor 本身不会改变这些访问要求。[Firecracker 恢复路径](AgentENV/src/sandbox/firecracker/sandbox.rs)

固定设备池/device plugin/CDI/DRA 是需要验证的候选机制，不能据名称保证运行中新增 minor 已获放行，也不宜无条件排除其他受控方案。接受“设备是阻断项”，不接受未经目标内核/CRI 验证就认为权限问题已经消失或已选定唯一实现路径。

### R5. 不采纳“原预热池收益完整保留、上传自然聚合去重”作为已有保证

对应 Claude §3.2 的收益比较。

当前 Firecracker warm pool 带有旧 NetworkManager 的 slot、网络命名空间和预启动进程。嵌入新的 per-actor 网络/cgroup 后，不能原样启用就认为归属正确。[pool.rs](AgentENV/src/sandbox/firecracker/pool.rs)、[pool 消费路径](AgentENV/src/sandbox/firecracker/sandbox.rs)

M1 关闭预热池，M2 验证同 Worker 受控领取、网络和 cgroup 归属后再开放。多 Actor 也不自动改变 atelet 的逐 Actor 快照上传；聚合、去重仍依赖新的共享层存储协议。相应潜在收益保留在性能假设中。

### R6. 不采纳将 roadmap 或经验数值当作已确定结果

对应 Claude §3.1、§3.9。

runtime modularity 和 Pod shape TODO 支持向上游讨论，但不足以断言具体补丁必然被接收。Go↔Rust UDS 的“预计亚毫秒、不构成风险”可以作为待测假设，不能作为验收结论。GPU 在本期范围外，也无需以未验证的未来趋势扩大当前建设范围。

已分别写入上游协作策略、性能测量项和能力范围；没有向上游提交 issue/PR，也没有建立自动升级任务。

## 5. 本次独立复核新增的补充意见

以下内容用于补齐原方案和 Claude 评审，不全部属于对 Claude 的反对意见。

| 编号 | 新发现／进一步限定 | 已落实处理与证据 |
|---|---|---|
| N1 | 删除旧 runtime HTTP 层会丢失原数据面授权执行点 | 迁移 secure/x-access-token 和 allowPublicTraffic/e2b-traffic-access-token 检查，在自动唤醒前执行，清理不应转发的凭据并防止入口绕过。见 §9.4；[runtime auth](AgentENV/src/api/impls/auth.rs)。 |
| N2 | 原生模板 UID 替换可触发隐式 DATA 恢复 | 仅检查模板 on_commit=FULL 不够，M1 必须拒绝该替换/恢复组合。见 §1.1、§17；[resume.go 中 TemplateReplaced 分支](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)。 |
| N3 | LOCAL pause 的保留引用不能只绑运行 session | pause 会释放会话，M2 必须给 LOCAL snapshot 独立 owner，避免会话结束触发误 GC。见 §8.3、§8.4；[pause workflow](substrate/cmd/ateapi/internal/controlapi/workflow_pause.go)。 |
| N4 | Revert 并不保证存在可恢复快照 | CRASHED→Revert→Resume 前检查恢复点；无快照的冷启动只能叫重建，不得冒充恢复旧状态。见 §12.5；[revert workflow](substrate/cmd/ateapi/internal/controlapi/workflow_revert.go)、[resume workflow](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go)。 |
| N5 | 后端 RESOURCE_EXHAUSTED 不一定自动进入重新调度 | 当前错误分类可能将非传输错误标 CRASHED，S5 需明确 NO_EFFECT 回滚、重选与 EFFECT_UNKNOWN 对账，不能只期待 router 重试。见 §4.3、§12.4；[crash.go](substrate/cmd/ateapi/internal/controlapi/crash.go)。 |
| N6 | pre-1.0 不默认支持新旧控制面协议滚动兼容 | 固定成套版本，使用隔离安装或协调升级；不能凭新 WorkerPool 金丝雀推导新旧 ateapi/atelet 可混跑。见 §14.2；[Substrate 开发约束](substrate/AGENTS.md)。 |
| N7 | per-actor cgroup 存在不等于完整内存/OOM 隔离 | 当前 leaf 主要设置 cpu.max；多 Actor 需补内存预算、计费与故障半径验证。见 §15；[actor.go](substrate/internal/ateomcgroup/actor.go)。 |
| N8 | CPU 共享配置并非必须保留的全局静态单例 | server 构造共享 Arc/RwLock 并注入 factory；保留注入边界，embedded 使用固定 domain，避免心跳改变旧快照要求。见 §5.2；[server.rs](AgentENV/src/bin/server.rs)。 |
| N9 | E2B 网络策略不是通用域名 allow/deny 的简单映射 | allowOut 支持域名/IP/CIDR，denyOut 仅 IP/CIDR，显式 allow 可覆盖用户 deny；Substrate hostname/protocol allow 规则不能等价覆盖全部语义。见 §9.5；[AgentENV policy](AgentENV/src/sandbox/network/policy.rs)、[Substrate policy](substrate/internal/egresspolicy/egresspolicy.go)。 |

## 6. 对 Claude 八项行动建议的完成状态

| Claude §6 行动项 | 回复状态 | 当前交付／剩余工作 |
|---|---|---|
| 1. 重新基线化与更新事实 | 已完成文档修订 | 两份文档固定 tag/SHA，更新多 Actor、调度、授权、scope 与源码链接。后续升级仍需重新核查。 |
| 2. 单／多 Actor ADR 与密度指标 | 已完成设计决策；性能待实测 | ADR-001 已写入，M2 优先有界多 Actor；定义密度/共享率指标，具体预算在 P0 后冻结。 |
| 3. 上传失败与 Worker 崩溃语义 | 已完成设计修订；实现待开发 | 区分普通错误、传输效果未知、Worker epoch 对账、无快照重建；不承诺现成暂存重试。 |
| 4. 设备、跨节点、daemon、envd 原型 | 接纳验证要求，尚未执行 | 已按 WorkerLocal/NodeBroker 分开，并保留两节点、身份和设备实验；没有将静态结论标为验证通过。 |
| 5. 出站映射和 E2B header 契约 | 已完成设计补充；兼容性待验证 | 增加 §9.4/§9.5，将能力限制前移到 M2 对外开放之前。 |
| 6. 复用现有机制 | 分条件落实到设计 | 复用 idle suspend/paused upload/Revert；parking 限定 router，HardwareIdentity/Authz 补齐后使用，不宣称已接通。 |
| 7. 工程估算和上游策略 | 已完成规划 | 人周、人员假设、依赖图、风险与补丁策略已补；不是交付承诺或上游接收承诺。 |
| 8. 文档质量、fork/GPU/威胁模型 | 已完成文档修订；验证仍开放 | 链接修复，维护风险、首期不支持范围和信任边界已补；安全加固与测试需在实施阶段完成。 |

## 7. 后续实施采用的决策

1. **M1a：** 单节点、单 Actor Worker、Worker 内设备服务、Full 独立包和原生 API，先验证生命周期及清理。
2. **M1b：** 两兼容节点跨 Worker 恢复，验证源 Pod 删除后仍可恢复、身份正确和故障处理；通过后才算 M1 完成。
3. **M2：** 有界多 Actor、同 Worker 设备共享、持久共享层/GC、E2B 明确子集；真实共享率、密度和尾延迟作为验收输入。
4. **可选增强：** 跨 Pod 设备 broker、统计超分、外部上传持久暂存重试单独论证和估算。
5. **M3：** 按实际需求增加评分/P2P、fork、构建与卷，保持 Substrate 唯一生命周期和分配权威。

Claude 的评审推动了必要的基线修正和架构重审；这些修改已进入两份设计文档。对被限定或纠正的内容，最终采用上文所述的代码事实与边界。剩余设备、性能和恢复验证必须通过原型与故障实验关闭，不能用评审之间的一致意见替代测试结果。

## 8. 对 Claude 第二轮评审的回复（v2.1）

评审日期：2026-10-08；本轮仍固定上述两个 tag/SHA，没有换到上游 main。重点复核第二轮新增八项建议及其调用链，而不是再次以第一轮回复证明自身正确。Claude 两份原始评审均保留不改。

### 8.1 总体判断

**需要更新设计，已经修订为 v2.1；保留整体架构，但不同意“剩余分歧为零、开放项全都只能实测”。** 认可从文档评审转向 P0 原型的方向，同时必须先明确可由代码和契约决定的资源准入、执行 fencing、出站兼容及隐式 VM 启动边界。

Claude 接受上一轮 R1–R6，有助于保持基线一致，但不能作为本轮结论的证据。本轮有一处明确的配置语义错误（未设置 Worker limits 不保证容量为 0），也有多处推论过强（一个 epoch 校验关闭竞态、域名 deny-all 应归入不支持、录制利用残留 token 等）。v2 自身也有遗漏：只明确关闭 FirecrackerPool，未点名第二层暖 slot 和自主 startup-pack 录制；入站契约未明确列出 template_builder 隐藏。v2.1 已补齐。

### 8.2 对第二轮八项建议的逐条裁决

| Claude §8 建议 | 裁决 | 依据与落实位置 |
|---|---|---|
| 1. 标注 idle suspend 未接线，引用 drain 时允许 checkpoint | **接纳事实，限定停机保证** | `ateomsuspend` 除库和测试外无调用方；checkpoint 放行不等于晚到请求有完成屏障。实现设计 §10.2/§15 增接线任务和删除 Pod 前的 drain 要求。 |
| 2. 区分 authn/authz | **接纳并补 handler 边界** | ateapi 先认证，缺口涉及已认证主体越权；AccessPolicy alwaysEnforce，RegisterWorker 另验 atelet 身份和 Node。§9.2/§15.1/§17 分开验收，不能把未登记一律说成没有检查。 |
| 3. 加 observed_epoch 即关闭绑定窗口、成本极低 | **部分接纳** | 对账水位校验有价值，但已有行锁盖 epoch；须另校验旧 claim、事务后重启、执行进程注册握手与 RPC fencing。§4 S5/§12.5/§17 已展开，不接受单点校验足够的推论。 |
| 4. 巡检 Revert 遗留 local checkpoint | **接纳** | `workflow_revert.go:90` TODO #641 明确未执行本地剪枝；§15 加文件/设备/owner 巡检，按无主证明回收，不能只看 DB 指针或 TTL。 |
| 5. 默认姿态不同，域名必需 deny-all 应列为常见 UNSUPPORTED | **接纳差异，拒绝后一处理建议和覆盖率推断** | 必需的 `0.0.0.0/0` 可以是 deny-all 基线；首批候选改为精确域名+该基线。通配符/默认 allow/混合 CIDR 另行拒绝；没有存量样本，不说“绝大多数不支持”。见 §9.5。 |
| 6. 同时处理两层暖池 | **接纳，保留正常重置路径的事实** | NetworkManager 确有 slot 缓存；正常 fresh/restore 会清空重建旧策略。“可能残留”的风险需测具体失败路径，不是仅凭池存在即确认泄露。§5.1/§17 同时覆盖两池。 |
| 7. Worker 侧显式校验 limits | **接纳建议，纠正论据** | 文件读失败→0 成立；容器未设置 limits→0 不成立，Downward API 有 Node allocatable 回退。§14.1 改为模板、最终 Pod、运行时三层校验。 |
| 8. 补跨节点 seed 风险和 startup_pack 证据 | **接纳 seed 风险，不采纳所述录制证明** | `access.rs` 的警告成立；录制复制宿主 config token 并 `start_nowait`，未验证 envd init。本轮新增禁用 embedded 自动录制的约束，见 §5.1/§5.3/§15.1。 |

### 8.3 关键分歧的源码证据

**一、Worker 未声明 limits 并不等于安全地上报 0。**

[register.go:62](substrate/internal/ateom/register.go) 从投影文件读取数值，文件缺失/不可解析才触发其兜底；[workerpool_apply.go:197](substrate/cmd/atecontroller/internal/controllers/workerpool_apply.go) 用 `resourceFieldRef` 投影容器 limits。Kubernetes 明确说明未设置容器 CPU/内存 limits 时回退 Node allocatable（[官方 Downward API 文档](https://kubernetes.io/docs/concepts/workloads/pods/downward-api/#fallback-information-for-resource-limits)）。这是两个不同层次，不能直接从文件读取注释推导 Pod 字段缺省行为。

由此推导的风险是：多个未限额 Worker 可能分别把整节点可分配量当成本 Worker 容量，不能靠零值默认保护。v2.1 强制显式资源配置，最终 Pod 与运行时均复核；目标集群的默认化和 cgroup 结果仍需实验。Actor 无 limits 时 `admittedResources=nil` 则确实不预留 CPU/内存，但“这是系统唯一的超分”过于绝对，K8S requests/limits 与 Node 预算仍是独立层面。

**二、epoch 已有事务保护，缺的是完整执行隔离。**

[BindActorToWorker:94](substrate/cmd/ateapi/internal/store/atepg/worker_assignment.go) 已在 Worker 行锁内读取 epoch 给 Assignment 盖章；[Resume:489](substrate/cmd/ateapi/internal/controlapi/workflow_resume.go) 还将锁内 epoch 复制到 Actor 的 WorkerAssignment。不能描述成完全没有绑定期 epoch 保护。

[observed_epoch](substrate/cmd/ateapi/internal/controlapi/workflow_reconcile_assignments.go) 表示对旧分配的对账完成水位。`validateAssignedWorker` 当前不检查 epoch，且重绑会更新旧 claim 的 epoch；这些是需要处理的具体缺口。但即使事务内比较相等，提交后仍可重启，controller 的 RestartCount 同步也可能滞后。必须有与本次执行进程绑定的握手身份以及副作用前的代际校验；拟新增 Rust executor 仅子进程重启时容器 RestartCount 不变，也须换执行实例身份。v2.1 将三段校验写入 S5/§12.5、将 `worker_instance_id` 贯穿 §6 的私有 RPC 与日志，并禁止持 Actor lease 等待需要同一 lease 的 reconciler，避免新增等待死锁。

**三、出站 deny-all 是可规范化的基线，通配符才有直接语义冲突。**

[validate_domain_allowlist:644](AgentENV/src/api/impls/sandbox.rs) 要求 `denyOut` **包含** `0.0.0.0/0`，不是“必须只有这一项”；首批兼容子集主动收窄为只有该项、无其他 CIDR。示例 `allowOut=["api.example.com"], denyOut=["0.0.0.0/0"]` 可以作为 Substrate 空 allow 基线+http:80/tls_passthrough:443 的候选，仍需 DNS、原始目的 IP、平台禁区和 IPv6 契约验证。

[AgentENV domain_matches:242](AgentENV/src/sandbox/network/policy.rs) 的 `*.example.com` 可匹配多层子域；[Substrate ParseHostnamePattern/Matches:310](substrate/internal/egresspolicy/egresspolicy.go) 只允许一层。v2.1 因此拒绝直接映射通配符。省略参数的 E2B Default 也不能悄悄变为默认拒绝；客户端必须明确接受平台受限模式，否则返回不支持。认可其兼容性影响，但“存量绝大多数输入被拒绝”缺少实际样本依据。

**四、draining 中允许请求，不等于提供 checkpoint 完成保证。**

[checkpoint.go:60](substrate/cmd/ateom-microvm/checkpoint.go) 确实允许停机时 checkpoint；但 [shutdown.go:68](substrate/cmd/ateom-microvm/shutdown.go) 的顺序是 WaitIdle→采集 guest→终止，没有阻止晚到 checkpoint 与 guest 终止并发的屏障。该结论来自控制流，不声称已复现实验故障。计划缩容在 Pod 删除前完成持久 Suspend；若实现 SIGTERM 后救援，需要显式协调协议及 deadline 失败语义。不能将“有 3600s grace”当作完成屏障。

**五、双暖池改造成立，原有规则重置不能遗漏。**

[NetworkManager::release:369](AgentENV/src/sandbox/network/manager.rs) 缓存 slot；但 fresh/restore 在 guest 运行前均调用 [set_egress_policy](AgentENV/src/sandbox/firecracker/sandbox.rs)，[slot.rs:459](AgentENV/src/sandbox/network/slot.rs) 保留旧规则标记，[policy.rs:223](AgentENV/src/sandbox/network/policy.rs) 清空重建 filter/NAT 用户规则。因此“归还时保温”只是待审查点，不足以认定正常交接会沿用旧租户权限。适配后的失败路径、代理连接、网络所有权和 cgroup 仍须专门测试。

Firecracker 在 [spawn_with_netns](AgentENV/src/sandbox/firecracker/instance.rs) 的启动路径进入 namespace，当前池条目不能靠改 TAP 文件名就迁给另一个 Actor。无需扩大为一般性的“所有多线程进程事后绝不可能换 namespace”论断；本设计只要求 spawn 前确定网络，或将整个已预分配会话由 adapter 正式转交。

**六、startup-pack 揭示的是额外执行路径，不能证明 token 重绑定。**

[startup_pack.rs:95](AgentENV/src/sandbox/firecracker/startup_pack.rs) 构造 recorder 并调用 `start_nowait`，之后只轮询 ublk daemon 的 recording 状态再 stop；[from_snapshot_config:708](AgentENV/src/sandbox/firecracker/sandbox.rs) 复制的是宿主 snapshot config 中的 token。实际 envd `wait_for_ready/init` 位于另一条完整 readiness 路径。因此不接受“故意利用快照内存残留 token 让录制 VM 连上 envd”作为已证明事实；内存快照可能保留源身份的风险及普通 Restore 的身份门控要求仍成立。

更直接的集成风险是：[SnapshotManager 发布路径](AgentENV/src/snapshot/manager.rs) 和 [template builder](AgentENV/src/template/builder.rs) 可发起录制，额外启动一个 VM。如果只关闭旧 Orchestrator 而直接复用这些函数，就可能绕过单 Actor 容量、执行归属和外部副作用约束。v2.1 初始禁用自动录制，允许按校验后的格式读取已有 pack；重新启用需纳入独立受控任务的身份、预算、网络、设备和清理账本。这是本轮新增实现约束，不只是 token 注释补充。

### 8.4 已更新文档与剩余工作

| 文档 | 本轮修改 |
|---|---|
| 软件实现设计 v2.1 | §4 S5；§5 双暖池/recorder/CPU restore/seed；§6 执行实例身份；§9 authn/authz、内部沙箱隐藏和网络子集；§10 idle 接线；§12 epoch 全链路；§14 资源准入；§15 drain/孤儿与风险；§17 新增 8 项验证场景；§21 修订索引 |
| 可行性分析 v2.1 | 同步身份授权、网络兼容范围，以及资源/epoch/自主录制/drain 的总体约束 |
| 本文 | 保留第一轮裁决，新增第二轮逐项回复及证据 |

整体 ADR、M1a/M1b/M2 分期和“Substrate 唯一生命周期权威”继续有效。支持推进 P0；资源/协议/策略契约可与设备实验并行落实，不能用原型性能结果替代正确性设计。两节点完整融合恢复仍是 M1b 门槛，P0 风险原型不等于提前完成全部 M1 集成。

不据本轮静态复核修改 25–41/16–28 人周为更精确工期；Claude 的“半天”最多可理解为文档编辑量，不代表上述实现与验证已完成。按 P0 证据重新估算，不因多轮意见一致而压缩故障注入要求。

本轮执行了源码/调用方检索、tag 与工作区核对、Kubernetes 官方语义查证、三份文档的交叉一致性及本地链接检查；没有改动项目源码或 Claude 原文，未编译、部署、运行 KVM/ublk 或性能实验。所有新增协议和准入规则都是待实现要求，不能视为当前 tag 的现成能力。


## 9. 批准实施后的源码记录（2026-10-08）

本次按用户批准的完整计划开始改动两仓库，不修改 Claude 原评审。此前“未修改源码、未编译”的结论只描述当时评审轮次，不再描述当前工作区。

已更新两份设计至 v2.3，修正 S9 仍标“可选”的冲突，记录普通证书方案、唯一网络管理者和最终范围。实际写入的组件、已通过测试及未完成项统一见 [实施状态与 F01–F11 验收账本](integration/README.md)。

当前裁决仍是：设计可以继续实施，但不得判为实现完成。Go class/协议/证书/客户端的局部测试通过，不足以证明端到端安全性和兼容性。Rust 日志与 FULL 包代码尚未编译；native adapter、控制面 fence 传递、bridge/catalog、capture/fork 工作流、builder、卷版本和通用安装等仍需要软件开发。这不是仅等待提供 K8S 环境的问题。

新增检查入口明确区分失败与跳过；本机节点预检按实际缺少 KVM/ublk 返回失败，Rust 检查因无工具链未执行，envtest 和整仓验证也未计入通过。保留“真实节点 + 普通集群 + F01–F11 对照全部完成后才宣布最终目标完成”的验收条件。


## 继续实施审视：执行链与引用管理

本轮根据实际代码修正了两个此前仅靠设计描述容易遗漏的问题：一是 Worker readiness 与查询 epoch 的启动循环，通过先创建身份记录、后验证执行端注册解决；二是旧分配重试可能被重写到新 Worker epoch，通过数据库分配代次、实例注册和生命周期协议贯通阻止。新增 Go adapter、网络所有权账本、进程监管和启动准备均有定向测试，已通过 Linux Go 交叉编译。

catalog 额外保留 owner 和运行实例停止记录，防止释放后晚到请求重新 pin；每次操作有自己的上传确认，避免 GC 删除对象后数据库提交失败时，新操作误用残留的 uploaded 标记。引用保留与 GC 使用同一层锁，未知操作结果不通过 TTL 自动释放。这些属于源码实现判断，数据库并发和真实运行效果仍未验证。

当前不能接纳“主要代码已完整、仅待环境”的结论。bridge HTTP/SDK 映射、typed EgressPolicy 确认分发、S3 接线、原生 capture/fork、builder、卷版本/写者排他、设备/cgroup 及普通 K8S 完整安装仍有实质软件工作。完整边界保持 F01–F11，不把中间 FULL-only 或拒绝未接线卷的行为作为最终交付。详见 [实施账本](integration/README.md)。


### 2026-10-09 实施核对补充

本次代码核对确认，Worker 记录消失和 epoch 更新只能证明控制面身份失效，不能证明原 Firecracker/ublk 已终止。AgentENV 的自动 crash/reconcile/delete 分配释放现已改为保守拒绝，直至明确停止或取得节点隔离证明。节点隔离证明的持久化与恢复工作流尚未完成，不能据此宣称故障恢复已经交付。

bridge 元数据模块已实现 PostgreSQL 外部 ID 映射、请求幂等结果和超时 revision；共享 catalog 与元数据模块可独立构建，本地纯逻辑测试通过，数据库合约测试未执行。HTTP bridge、SDK 全功能路由、模板/卷工作流以及真实集群验收仍未完成。停止重试、宿主开销预算和执行端清理错误传播的最新实现与验证明细见 `integration/README.md`。开发继续按 F01–F11 完整范围推进。


### 2026-10-09 继续实现同步：兼容服务与运行凭据

兼容层已新增可启动服务、删除/续期路由、持久超时删除意图与回收循环；仍不代表创建、connect、构建、fork、卷等 SDK 路由已全部实现。超时 claim 与续期锁定相同元数据行，删除 RPC 结果未知时保留 claim，禁止续期复活。成功删除后原子记录 tombstone；PostgreSQL 合约尚未实测。

新增 Substrate `ConnectActor(actor, uid)` → atelet `ReadAgentENVConnection`，在 Actor lease 和完整 Worker fence 核对后读取节点私有 envd 凭据。要求 Actor 更新权限，凭据只用于当前分配，不写入 bridge 请求回执或快照。生命周期与 EgressPolicy CRUD 同步补齐父 Actor 授权规则；安装必须启用现有授权 enforcement。

FULL 工具盘依赖已在 Rust 源码补为包内只读 tools 镜像与摘要校验，仍待 Linux Rust 构建及设备测试。SDK 版本已锁定且本地安装通过，基础 Python/TypeScript 对照入口已写入，尚无真实后端执行结果。完整实现及 F01–F11 验收均未完成；详细状态以 `integration/README.md` 为准。

后续验证更新：已在临时 PostgreSQL 18.0 上运行并通过 bridge 全包 race 测试（含 catalog、元数据与超时并发合约），Substrate 授权权限矩阵也已在实际 PostgreSQL/OpenFGA 数据层通过。新增内部在线 Capture 通路，保留源运行资源并隔离每次导出，Go 通路测试通过；公共捕获提交、fork 和导出回收仍未完成。Rust 编译、真实设备和 K8S 验收仍待进行。这些结果不改变“整体尚未完成”的结论。

控制面真实 PostgreSQL 回归后续通过：曾发现 epoch 严格门控误影响原 gVisor 的 4 个场景，已将额外门控限定于 AgentENV，并用真实数据库验证旧 AgentENV claim 仍不可重新绑定或覆写。恢复源码还修正了未显式覆盖时应保留快照网络策略、扩展参数及环境变量的语义；该 Rust 改动待编译执行。


### 2026-10-09：生命周期 UID 与普通 K8S 证书部署补充

- SuspendActor/ResumeActor 增加可选 UID 前置条件；bridge 必须传入已持久化的 Actor UID。控制面在租约内再次校验，Resume 的只读 RUNNING 快路径也校验，避免先 Get 再按名称变更造成同名新实例误操作。旧后端未设置 UID 时保持原有按名称语义。新增真实 PostgreSQL 用例验证旧 UID 拒绝、当前 UID 幂等和租约获取前后实例变更。
- 新增 `substrate/cmd/ate-generic-manifests`，转换 API/controller/atelet/router/egress 的实验性证书投影为标准 token + init + 普通 sidecar；启用授权与 ConfigMap/file 信任根 provider。新增 `ate-identity-bootstrap` 生成分离的 issuer TLS/Pod CA、私有 Secret 和公共根 ConfigMap，0600 文件独占创建，不覆盖已有密钥。部署 RBAC/issuer 清单及具体步骤见 `substrate/manifests/ate-install/generic/README.md`。
- 轮换 agent 增加仅允许 0600/0640 的文件模式，通用控制面使用专用共享组读取原子替换后的证书；默认 Worker 仍为 0600。五类原清单转换、证书模式、bootstrap 信任链与拒绝覆盖的 Go race 测试通过。尚未在真实 Kubernetes 验证。
- 这不是完整安装器：PostgreSQL/OIDC 权限、对象存储、节点镜像凭据配置、运行资产和 Gateway/bridge 接线仍需整合；尚存的 SDK、公共 capture/fork、构建和卷功能缺口未因本次工作消除。


### 2026-10-09：网络策略私有执行确认链路

- canonical executor proto 新增 `ApplyNetworkPolicyRequest/Response`，atelet/ateom 共用这份类型。atelet 对照本地 preparation 验证完整分配 fence，再原样转交 operation ID 和 policy；Worker 通过已有持久 journal 调用 Rust `UpdatePolicy`。
- 成功必须匹配请求 revision；执行结果未知、缺少 ACK 或 ACK revision 不匹配均返回错误。策略更新不重建 netns/TAP，不撤销 ingress，不修改启动重试的不可变 preparation。
- 新增 envelope 限制及真实 UDS 转发测试；旧 epoch/assignment 拒绝、payload 保持、未知结果和 revision ACK 的 Go race 测试通过。Rust 实际策略执行仍待 Linux 编译和节点测试。公共 EgressPolicy 类型、持久化 delivery intent、后台重试与启动/恢复注入尚未接通，不能把本项作为 F05 通过。


### 2026-10-09：公共 AgentENV EgressPolicy 交付流程

在现有 EgressPolicy CRUD 中加入 `agentenv` 类型，保留原网关 rules；两者互斥，策略后端必须匹配 Actor，更新不能转换后端。公共校验沿用原 AgentENV 的 Default/Allow/Deny、IP/CIDR、allowOut 域名及前缀通配符，denyOut 仅接受 IP/CIDR。

ActorStatus 中保存 server-owned delivery projection：单调 revision、期望策略、资源 UID/version、最后确认的 assignment/revision 和删除意图。CRUD 持有 Actor lease；先持久化期望状态，再调用 atelet/Worker，只有匹配 ACK 才确认运行中策略。暂停实例只提交期望状态，不伪造运行时确认；Run/Restore 在 VM 启动前注入策略，并将策略绑定到不可变 preparation digest。恢复缺少显式 override 时仍保留快照策略。

删除先持久化 Default 交付意图，确认后才删除 EgressPolicy 行；后台按页对账，修复 API 重启、策略行已写但 intent 未写、ACK 丢失、策略行已删但删除意图未完成等窗口。旧 revision 不因策略资源删除重建而复用。对账不会通过新建 VM 处理失败，不释放不确定的分配。公共读取不会将旧资源版本的 ACK 附着到新版本。

已通过私有 UDS/ACK、真实 PG CRUD、失败重试和生产 ServiceImpl 校验路径的定向测试。Rust/Linux 真正策略生效、SDK bridge 的 allowInternetAccess/network 映射及 F05 对照验收仍未完成，整体实现与最终目标仍未完成。


### 2026-10-09 实施补充：策略更新执行确认

公共 AgentENV EgressPolicy 已增加持久 revision、执行确认及恢复对账；SDK `PUT /sandboxes/{sandboxID}/network` 已接入。Actor UID 与 RUNNING 前置条件在控制面生命周期 lease 内检查，暂停状态或旧 Actor 身份不能先写入策略。相同策略重试复用 revision；bridge 收到匹配 policy UID/version 的执行 ACK 后才返回成功。原版 API 的 IPv4 限制、域名 allowOut 需要显式 denyOut `0.0.0.0/0` 的约束已保留。

Go 定向测试（含真实 PostgreSQL）通过，不代表 F05 最终验收。并发历史 pending 请求已通过持久化预期 revision 与控制面乐观检查防止覆盖新策略（含删除后重建）；相关真实 PG 回归通过。Rust/Linux 实际网络、完整 SDK 对照和普通 K8S 验证未完成；总体软件实现也仍有公共 capture/fork、构建、卷及其余 SDK 路由等缺口。详见 integration/README.md 的当前状态表。


2026-10-09 指标接口补充：新增 `GetActorGuestMetrics`，使用 Actor 读取权限、Actor UID 和完整 Worker 分配身份读取当前 envd guest 样本。CPU 百分比、guest 内存与宿主机 cgroup 计量保持语义区分。SDK 历史指标 GET 路由与后台采样随后已接入：15 秒采样、1 小时保留、PostgreSQL 去重及租户/分配身份隔离，相关数据库与 HTTP 测试通过。历史可跨 bridge 重启保留，与原版节点本地历史有差异，需纳入对照报告。逐 Actor cgroup 限制、真实 envd/VM 指标验收仍待补齐，不作为 F10 完成依据。验证状态见 integration/README.md。


2026-10-09 暂停实施补充：SDK pause 已映射持久 Suspend，新增可选 assignment generation 前置条件防止旧暂停请求影响恢复后的新分配。兼容数据库 v2 持久暂停意图同时阻止超时回收，后台重试保留分配身份；只有确认 SUSPENDED 及快照 URI 才回复成功。相关真实数据库并发、Go race、静态检查和构建通过。恢复后定时器提交、SDK autoResume 与原生路由恢复策略仍需补齐；不宣称 F01/F06 已验收。


### 2026-10-09：持久恢复、SDK 连接和运行时连接信息补充

已接通 bridge 的 v1/v2 connect 与 legacy resume 路由。恢复意图在 PostgreSQL metadata v3 中持久化，绑定 Actor UID 和源快照 URI；控制面在读取快路径及生命周期租约内检查源快照，拒绝旧快照请求影响后续暂停产生的新快照。持久恢复没有快照时返回错误，不创建空白实例替代。未知执行结果保留 timer hold 并由后台重试，只有确认 RUNNING 才原子移除暂停/恢复 hold、提交 TTL 和完成回执。重复完成回执不会再次延长 TTL；运行中 connect 只延长、不缩短现有到期时间。

SDK 连接响应的 envdVersion 从当前 executor 的实际启动配置读取，envdAccessToken 从当前分配的私有 preparation 读取；读取链路校验 Worker Pod UID、epoch、executor 实例、assignment generation 和运行状态。连接 token 不写入兼容 metadata。v1 timeout 范围按原源码修正为 0..4294967295 秒，0 表示立即到期；v2 connect 默认 300 秒、最小 1 秒，legacy resume 默认 15 秒。bridge 必须配置 sandboxDomain。

验证状态：真实 PostgreSQL 的 schema 升级、恢复意图/到期互斥、重复完成和 TTL 提交测试通过；bridge 全包 race、vet 和 Linux amd64 构建通过；Substrate controlapi、apivalidation、authz、atelet、adapter、executor 相关全包 race、vet 和 Linux 构建通过。Rust 的运行时版本读取只执行 rustfmt，尚未 cargo check/test。SDK 命令/文件/端口的数据入口仍未接通；到期 autoPause、访问触发 autoResume 的完整策略仍待实现。连接 JSON 返回成功不能作为 F01–F11 或真实 SDK 验收通过。


### 2026-10-09：入站 Actor 身份约束

Substrate 路由解析后向 Worker 写入控制面返回的 `ate-target-actor-uid`，覆盖外部输入；Worker 在取得当前 activation 的同一把锁内核对 UID，拒绝已删除并同名重建实例的旧请求，返回 421/stale assignment。该内部头在转发 guest 前移除。Gateway 的 Substrate 模式同时移除客户端提供的 Actor、UID 和目标端口路由头，后续由可信 bridge 重新生成。原有未携带 UID 的内部调用保留既有行为。

Gateway 转发隔离、Worker 当前/旧/重复/空 UID、UID 不泄漏到 guest，以及路由覆盖伪造 UID 的 race 回归通过。此保护限定为 Actor incarnation；完整 assignment generation 路由约束、SDK 数据代理和 autoResume 控制仍需继续实现，不能据此声明完整入站验收通过。


### 2026-10-09：SDK 数据代理、自动生命周期与基础创建

- bridge 已接入沙箱 host 与 `/proxy` 数据路由，分开验证 secure envd 与应用端口的 public/traffic token；先认证再自动恢复。SDK 得到稳定、绑定 tenant/external ID/Actor UID 的 HMAC 凭据；代理在每次分配读取后注入新的 guest token，凭据不写入 metadata。原生 runtime token 不再作为融合数据入口的客户端凭据。bridge 副本必须共享同一 32 字节 key Secret。
- bridge 固定连接内部 Substrate HTTPS ingress；携带 Actor UID、assignment generation 和端口。带 generation 的路由只 GetActor，不隐式 Resume；Worker 在 activation 锁内核对 UID/generation。Gateway 移除外部路由头，所有代理移除平台凭据后转发 guest。内部 ingress 必须以 NetworkPolicy 限制来源，不能对外暴露原生无认证路由。
- HTTP 请求体与响应可同时流式传输，WebSocket 101 升级和双向字节透传已有实现及真实本地 HTTP/TLS 测试；这尚不是 envd/SDK/K8S 验收。
- metadata v4/v5 增加不可变访问/生命周期选项；到期 autoPause 将已提交 expiry claim 原子转为冻结 generation 的 suspend hold，未知结果后台重试，不改为删除。autoPause=false 执行删除。自动恢复只发生在已认证且 autoResume=true 的暂停实例上。
- metadata v6 保存创建 job、固定启动描述和兼容 profile。创建基于明确配置的 native template/profile，冻结私有模板传递 envVars 与初始扩展参数；跨 bridge 创建/删除串行化，已绑定 UID 不通过 Create 重建。创建期间持有到期与数据入口 hold；确认 RUNNING 后提交 profile、runtime envdVersion 并开始 TTL。SDK v1/v2 create、list/info 路由已接入，列表支持状态、metadata、时间、模板、排序和游标过滤，状态仍从 Substrate 读取。
- 初始 extension params 已加入公共 Container、私有 WorkloadSpec 和 LaunchSpec，限定 AgentENV 后端、JSON object、大小上限并标记 debug_redact；已通过官方生成脚本和初始参数校验/传递测试。

验证：数据认证/隔离、流式/WebSocket、自动暂停、创建重试、列表/详情等 Go race 测试已通过；metadata 的创建 hold、确认、迁移与自动暂停在真实 PostgreSQL 定向回归通过。后续合并检查仍在进行。真实 Linux/Rust、envd SDK、S3 和 K8S 未执行。公共 capture/fork、动态模板构建/导入与 GC、卷、运行中 extension update、共享层、设备/cgroup 完整管理及成套集群交付仍有实际代码缺口，整体尚未完成。


### 2026-10-09：原生在线捕获及快照子实例恢复路径

新增公共 `CaptureActorSnapshot(actor, uid, assignment_generation, tag_name) -> Tag`，授权要求源 Actor 的 can_update。Actor lease 与 Tag lease 贯穿在线捕获；Tag 保存源 UID、完整 assignment 和冻结的执行请求，以 Tag UID 的独立 FULL 对象前缀作为目标。未知执行结果保留 pending Tag，同名同源重试使用原始请求；确认上传完成后才提交 Snapshot，不改变源 Actor 状态。已完成请求不重新执行。执行载荷只保存在控制面，公共 Tag 响应清除该载荷。

创建任意 Tag 来源的 Actor 时持有 Tag lease 直到引用入库；删除 Tag 前遍历所有 atespace 的 Actor 引用，仍被借用时拒绝删除，查询失败也拒绝回收。在线捕获未确认时禁止删除目标，避免晚到上传与 GC 竞争。后续仍需实现物理隔离后的失败捕获清理与 catalog 对账，不以直接删除 pending Tag 代替。

bridge 增加租户/分配约束的 Capture 调用及 CreateFromSnapshot，创建 job 支持共享捕获模板而非重新克隆模板，保留模板 UID 校验、每个子项独立绑定 Actor UID、策略注入、恢复与 RUNNING 后 TTL 提交。批量 fork API、父操作持久 saga 和失败子项回收尚未接入，不能将此子实例路径记为 F07 完成。

验证：新增在线捕获重试、冻结请求、公共响应载荷清除、pending 删除拒绝和借用引用保护已在真实本机 PostgreSQL + bufconn 上通过 race；bridge Capture 身份检查及快照子实例独立确认测试通过 race，公共请求/授权校验通过，相关 vet 通过。完整控制面回归正在执行。真实 VM 在线捕获、S3 上传、跨 Worker 恢复与 SDK fork 未执行。整体功能编码仍未完成，环境限制与实际代码缺口分别记录。

追加约束：显式快照子实例使用不可变 `Actor.source_tag_uid`，控制面在同一 Tag lease 下校验源 Tag UID，bridge 在创建重试时同时核对模板、Tag 名和 UID，拒绝同名 Tag 删除重建导致的来源替换。最新完整控制面 PostgreSQL race 回归通过（controlapi 102.790s，authz、apivalidation 通过）；新增 UID 定向回归与 Linux 构建另行记录结果。

验证补充：Tag UID 约束及 capture/借用保护定向 PostgreSQL race 回归通过（5.079s）；bridge control/creation race 通过；ateapi 与 aenv-api-bridge Linux amd64 构建均 exit 0。运行中扩展、批量 fork saga、构建/卷/共享层及集群交付等剩余代码工作未因此标记完成。


### 2026-10-09：批量 fork 持久编排与扩展执行链

**已编码的 fork 路径：** bridge 接入 `POST /sandboxes/{sandboxID}/fork`，支持 count 1–100（默认 1）、可选 u32 timeout（缺省继承父实例）和 Idempotency-Key。metadata v7 新增 fork_jobs / fork_children；冻结源 UID/generation、捕获目标、模板、网络策略、访问选项、profile 和确定性 UUID 子项 ID。未确认捕获时保持到期与 SDK 持久暂停互斥；捕获确认后持久保存完整 Tag 描述，再独立恢复子实例，父 Actor 不必继续存在。后台循环在 bridge 重启或 HTTP 超时后继续未完成任务。

每个子项使用独立 creation job，由 Substrate 分配新 UID 和 Worker；模板与 Tag UID 均受约束，TTL 从该子项确认 RUNNING 后开始。成功结果和已清理的失败结果逐项提交，父操作提交失败不会重做已完成子项。永久启动失败仅在冻结创建 job、停止后续创建重试并确认 UID-bound 删除后进入失败结果；超时/断连及无法确认 UID 的清理保持 pending。已确认后立即到期/删除的子项仍能从创建 receipt 判断完成，不会被重新创建。网络策略尚未执行确认时拒绝开启新的 fork。结果 receipt 不保存 bearer credential，HTTP 返回时重新读取兼容信息。

**扩展参数补充：** 私有协议新增批准参数与当前参数返回，Go adapter / atelet 接入 ApplyExtensionParams；延续同一持久 operation ID、分配 fence 和 executor journal。确定 NO_EFFECT 以类型化响应传回，未知执行结果不当作批准。Rust 在开始副作用前检查运行状态、hook 配置和 JSON object；执行后返回 hook 批准的完整参数，Inspect 读取 runtime 的当前参数。恢复时清除 initial-template extension override，使用快照保存的已批准参数，避免 pause/restore/fork 回退到初始值。此处仅完成私有执行链；公共控制面意图、批准参数持久化和 SDK GET/PATCH 路由尚待接入，不能记为完整 F10 已完成。

**验证：** bridge 全包 race 测试通过，强制执行真实本机 PostgreSQL（包括 v1→v7 迁移、捕获确认、到期/暂停互斥、逐子项结果不可变、失败清理及已完成子项删除后的确认）。fork 定向测试覆盖源 generation 改变、未知捕获/启动结果、部分失败、父 receipt 提交失败、重复 key 和已到期子项不复活；HTTP 兼容响应与输入校验通过。private executor / adapter / atelet 的扩展参数校验、批准 ACK、NO_EFFECT、旧分配拒绝和恢复状态保护 race 回归通过。Go vet 与 Linux amd64 构建通过；Rust 仅 rustfmt，未 cargo check/test。真实 VM/S3、带卷 fork、SDK/CLI 和 K8S 验收未执行。

**仍需编码/接入：** fork Tag 的 catalog operation pin 与最终回收、模板构建/导入/刷新和 builder 管理、公开卷生命周期/写者排他/版本、公共运行中扩展更新、共享只读层及完整设备/cgroup 管理、成套普通 K8S 安装与运维脚本。当前 fork Tag 保持独立持久所有权，不启用未接通的自动 GC；它仍被 Actor 借用时控制面禁止删除。整体功能开发和最终验收均未宣布完成。


### 2026-10-09：扩展参数公共控制面与持久确认

新增 Substrate `GetActorExtensionParams` / `UpdateActorExtensionParams` RPC，沿用 Actor 的读取/更新授权；更新必须提供 Actor UID、assignment generation、operation ID、expected revision 和不超过 64 KiB 的 JSON object。控制面在 Actor 生命周期 lease 下先保存完整更新请求及原 WorkerAssignment，再调用既有私有扩展执行链。只持久化 hook 批准的完整参数；传输错误或未知结果保留 pending，后台扫描按冻结分配重试，不重新定向到新进程/新 assignment。明确 `NO_EFFECT` 保存拒绝回执，不推进参数 revision。相同 operation ID 的请求内容必须一致。

初次读取从 fenced runtime Inspect 获取实际参数，包含 snapshot/fork 继承状态；已经观察到的参数可在持久 suspend 后读取。尚未观察过、已处于 suspend 的历史 Actor 需要先恢复完成首次观察，不能用模板默认值冒充快照中的参数。pending 更新阻止 pause、suspend 和在线 capture。显式 revert 完成终止及分配释放后清除运行参数缓存，恢复后重新观察快照中的状态。

本次增加 atelet 的 fenced `ReadRuntimeInfo` 转发、协议生成、参数脱敏及未知结果/幂等/拒绝/旧分配测试。SDK `/custom-extension-params` GET/PATCH 路由及 bridge 请求后台重放仍待实现，不能将公共控制面完成记作 F10 全链验收完成。模板构建、附加卷、共享层/catalog 引用闭环及完整普通 K8S 安装仍有功能编码工作；真实 Rust/Linux VM/K8S 验证继续单列。

验证记录：扩展专项 `go test -race` 使用真实临时 PostgreSQL 通过（含旧 allocation 不重定向）；官方协议生成、相关 `go vet`、`ateapi` 和 `atelet` Linux amd64 构建通过。相关整包回归运行结果见后续记录；Rust Cargo/VM/SDK/K8S 未执行，不能计为通过。


### 2026-10-09：SDK 扩展参数路由与 bridge 持久重放

本次补齐 SDK `GET/PATCH /sandboxes/{sandboxID}/custom-extension-params`，响应为 hook 批准的完整 JSON object。PATCH 不做参数合并，转发对象补丁，保留 JSON 数值精度。沿用租户鉴权、已确认创建检查和 Actor UID 映射。Idempotency-Key 按 tenant/sandbox 隔离，同一 key 的不同参数拒绝；重复完成请求返回持久 receipt，不重复执行 hook。

metadata v8 新增 extension_jobs，冻结公共控制面请求中的 UID、generation、operation ID、expected revision 和 patch。请求与到期 hold 原子提交；后台循环在 bridge 重启、HTTP 超时或结果提交失败后重放同一请求，不采用新的分配或参数 revision。只确认控制面返回的完整批准结果，或与完整冻结请求匹配的持久 NO_EFFECT 回执；未知结果继续 pending。NO_EFFECT 兼容原版 HTTP 400。pending 扩展与到期回收、持久暂停、未完成 fork capture 双向互斥，明确批准/拒绝后释放 hold；显式删除仍通过 Substrate 执行。

创建和快照子项创建在确认成功、开始 SDK TTL 前查询实际 runtime 扩展状态并持久化，因此新实例第一次 suspend 后也可 GET 已批准参数。历史 suspended Actor 未曾观察过 runtime 参数时仍需先恢复完成观察，不能用模板值冒充快照中的状态。本条更新取代前文“SDK 扩展路由及 bridge 重放未实现”的当前状态判断，保留前文作为历史进展记录。

验证：上一轮 Substrate controlapi/apivalidation/authz/atelet 整包 race（真实临时 PostgreSQL）全部通过；本轮 bridge 全包 race（mandatory PostgreSQL）通过，最后增补创建观察、HTTP HEAD 及 v7→v8 迁移的相关回归也通过。Go vet 和 bridge Linux amd64 构建通过；Rust Cargo、实际 hook/VM、SDK 原版对照、S3、K8S 未执行。模板构建/导入、卷、共享层/catalog 生命周期接线、设备/cgroup 和完整普通 K8S 交付仍有功能编码工作，整体尚未完成。


### 2026-10-09：逐 Actor cgroup 限制、账本及宿主机指标

Worker 新增 mandatory cgroup v2 执行链。启动时从 `/proc/self/cgroup` 定位当前容器 scope，验证它包含当前 Worker PID 且 CPU/内存有有限上限；特权容器继承宿主 cgroup namespace 时使用所属容器子路径，不直接在宿主根创建 Actor 组。把 scope 中的 Worker 进程移入 `aenv-supervisor` 子组，再严格启用 cpu/memory/pids；缺少权限、控制器或有限 Worker limits 时启动失败，不降级为无隔离运行。既有特权 Worker/AppArmor 配置需要允许当前容器 mount namespace 内的 cgroup writable remount，不新增 host cgroup 挂载。

Go adapter 在网络及执行 RPC 前持久预留完整 fence、机器规格和 cgroup 路径，配置 cpu.max=(整数 vCPU + 已计入预算的 host CPU reserve)、memory.max=(guest MiB + host memory reserve)、memory.swap.max=0、memory.oom.group=1、pids.max=4096。默认 CPU/memory reserve 沿用 250 milliCPU/128 MiB，Worker 自身及共享 ublk 服务仍在 Worker overhead 范围。配置中断的记录不标 ready；重试不得跳过未完成的限制、重塑运行组或认领没有账本的组。Stop/capture-stop 的 executor ACK 后，必须确认 cgroup.events populated=0 才移除组和 fsync 账本。未知执行/停止结果保留分配；残留进程、不明组或资源状态不清楚时停止回收。

私有 LaunchSpec 携带 adapter 覆盖的 cgroup_path；Rust 检查它与指定 cgroup root、Actor UID 和 assignment generation 完全一致。Firecracker 子进程使用预先打开的 cgroup.procs，在 exec 前通过 child-only syscall 入组，避免把共享 executor/launcher 线程移入某个 Actor。普通 standalone 使用 None 保持原路径。路径在 common config 标记 serde skip，FULL 快照不携带源节点 cgroup 路径，恢复按新分配注入。新 executor 的 fenced Reconcile 确认无 Actor，且其已有 live journal 启动屏障通过后，Go 才清理空闲残留组；这不是“进程重启即证明 VM 已停止”，也不替代节点物理 fencing 或设备对账。

原 gVisor cgroupstats 解析器移至共享 internal/cgroupstats，两后端复用，gVisor 只更新 import。AgentENV 接通 GetWorkloadStats / GetActiveWorkloadStats，返回带归属和 activation epoch 的 VMM cgroup current/peak/working-set bytes、累计 CPU usec，标记 CGROUP/AGENTENV；不冒充 guest envd 样本。统计不占 Actor lifecycle 锁，分配改变时不沿用旧归属，采样失败在 discovery 中返回未测量而不是伪造零值有效样本。

验证：Go adapter/admission/cgroup manager/network/supervisor/executor/shared parser race 回归通过；Worker Linux amd64 构建、cgroup 内核测试 Linux 编译、gVisor 统计测试 Linux 编译及 Go vet 通过；节点预检 3 项 Python 单测通过。内核测试要求 AENV_CGROUP_TEST_ROOT 指定已准备的隔离 delegated child scope；CI 或 AENV_REQUIRE_CGROUP_TESTS=true 缺条件时失败。已实际验证本机 CI 严格分支失败，本机 native cgroup 测试为未执行。verify-source.sh 强制该条件，新增 Rust child-scope 和运行路径不入快照的测试源码并 rustfmt；Rust Cargo、真实 kernel/VM 内存与 CPU 压力、多 Actor OOM 隔离及节点回收没有执行，不能计为通过。

设备账本/故障对账、物理节点 fencing、模板构建/卷/共享层/catalog 完整生命周期及普通 K8S 成套交付仍有编码工作。本次补齐 cgroup 和 host stats 路径，不宣布整体实现或 F01–F11 验收完成。

补充：统计 epoch 与 cgroup 初始预留一起持久化，重试及重启读取同一值，不在启动完成后重置以免错归冷启动 CPU 开销。Worker 在注册前强制检查 executor 的 allocation-cgroup-v2 capability 及进程实例 ID；缺少能力时拒绝启动，防止误用旧二进制而忽略新的 cgroup_path。最终 epoch 回归、Worker Linux 构建及 Linux 静态检查通过。


### 2026-10-09：设备会话持久屏障与可信退出确认

executor 的启动顺序调整为：打开并锁定 RocksDB journal、拒绝旧 `live/` 或 `devices/` 记录、绑定独占 UDS、同步持久化 `devices/session`，最后初始化 ublk daemon。这样没有创建过 Actor 的预热设备也在持久所有权范围内；同一 endpoint 的第二个进程不会先启动设备服务。daemon 初始化失败或进程异常退出保留 session 和 socket，不自动删日志、接管旧设备或将进程重启当作停止证明。

正常关闭先 drain/停止全部 Actor，再停止 daemon。daemon 停止接收连接后等待已有请求结束，停止继续调度 pool refill，并等待已有 refill 结束，再枚举并删除设备，避免并发 create/refill 在清理枚举后发布设备。清理所有设备时累积删除错误，仍尝试清理其他项，最后以错误退出；此前 pooled device 删除失败或 startup rollback 无法确认也使退出失败。client 等待子进程退出并检查成功 exit status，等待超时、异常/未知退出不返回成功。只有上述确认后 executor 才同步删除 session 并移除自己的 socket；失败保留屏障，要求隔离旧 Worker/节点后按恢复流程处理。

新增 Actor-less 异常重启、正常释放、重复 claim/release、endpoint 冲突不抢占和未知 device session 拒绝绑定测试；daemon client 增加失败/未知退出及等待真实子进程清理的测试，修改旧“无响应即成功”测试为拒绝未知清理。Linux verify-source.sh 增加 executor binary 和 daemon shutdown 测试入口。

本次完成的是共享设备会话所有权及关闭屏障，不是完整逐设备 ID/Actor 引用账本、设备数量预算、节点 fencing 证明或自动重建。它也不提供故障时对遗留 kernel device 的自动删除；不确定时保留分配。上述功能，以及模板构建/导入、卷、共享层/catalog 生命周期与完整普通 K8S 安装仍需编码。

验证状态：修改的 Rust 文件 rustfmt/格式检查及 git diff --check 通过；verify-source.sh 的 bash 语法检查通过。本机没有可用 cargo/Linux ublk 环境，因此新增 Rust 测试、daemon 编译、内核删除故障及多 Actor/真实集群验证均未执行，不能计为通过。整体实现尚未完成。


### 2026-10-09：逐 kernel device 持久账本与 Worker 设备预算

新增 daemon `device_ledger`，embedded 模式必须启用。它在 ADD_DEV 之前同步写入预留；通过 ublk builder 的可选 on_allocated hook，在 ADD_DEV 确认 ID 后、udev 等待和 char device 打开之前同步记录 kernel device ID。记录包含 daemon 进程所有者、创建时的 image config 路径和 operation 序号。未知创建、future/handle 被丢弃或持久化失败不自动回滚。账本通过 mutex 限制同一 Worker 的物理设备总数，shared read-only、运行设备及 idle/prewarm pool 都计入；共享设备按实际设备数计一次。目录/文件分别为 0700/0600，排他 flock 防止两个 daemon 使用同一账本，fsync 文件、rename 和目录保证提交顺序。

普通删除、pooled 删除、启动失败回滚和 daemon 关闭全部接入账本；仅 DEL_DEV 成功或回滚明确 ENODEV 后释放相应记录并同步目录。未知 ID、重复 ID、删除/持久化失败保留未确认状态，daemon 不给出成功关闭确认。最后关闭还必须确认内存账本和目录均无剩余记录，包含未知 ID 的 intent 与中断写入的 pending 文件。启动发现旧记录、pending 文件或不明目录项时拒绝接管，不扫描节点全局设备列表、不删除其他 Worker 的设备，也不盲目重试旧 kernel ID。

WorkerPool `spec.agentenv.maxDevices` 默认 64，CRD 范围 4–65536，controller 显式传给 Go Worker，再传给 executor/daemon。executor capabilities 新增 max_devices 和 durable-device-ledger-v1，Worker 注册前强制核对能力与配置。embedded 预热池 high watermark 限制为设备预算的一半，low watermark 相应收紧，为运行/root/tools/recording 留出空间；client 即使带 --config 也发送明确 pool overrides，避免 TOML 悄悄覆盖预算。standalone 不启用新 ledger 时保留原执行路径，原 builder 调用不配置 ownership hook。

当前账本是 Worker/daemon 对实际 kernel device 的持久所有权与预算，image 字段是创建来源，不作为当前 catalog 引用或 Actor 写者身份。Actor 分配仍由已有完整 fence/live journal 管理，共享设备引用仍使用已有 refcount/pool 逻辑。逐 Actor/角色的持久引用、断电/节点隔离证明及自动恢复对账仍需接入；本次不将目录删除或更换 Pod 当作节点 fencing。设备预算不是节点全局 Broker 的容量保证，同节点多个 Worker 的总预算需由部署配置与实际设备能力共同约束。

验证：官方 codegen 通过，生成新私有协议和 CRD；controller Pod 配置专项 race（包括默认/指定 maxDevices）、Go executor/adapter/admission/cgroup/network/supervisor race 回归通过，Worker Linux amd64 构建和 Linux Go vet 通过。新增 Rust ledger 并发预算、未知创建重启、明确删除、重复 kernel ID、排他锁及部分写入测试源码；Rust 文件格式检查通过。cargo/Linux ublk 环境缺失，Rust 编译/新增测试、真实 kernel quota/udev/删除故障、standalone 和多 Worker 节点验证未执行，不能计为通过。模板构建、卷、catalog/共享层完整生命周期及成套 K8S 交付仍有编码工作，整体尚未完成。

补充：任何账本持久化/释放提交结果未知时，将当前 daemon 账本标记 uncertain 并停止新的设备预留；即使后来清空已知记录，也不能在这个进程内返回正常关闭确认。它防止删除已执行但目录 fsync 未确认时，继续创建并复用同一 kernel ID。新增失败预留测试使用仍有剩余容量的预算，明确验证未知状态拒绝继续分配。


### 2026-10-09：持久模板目录与 SDK 模板读取

bridge 新增独立 v1 template registry migration，与 metadata v8/catalog/metrics 的迁移及 checksum 分开管理。已预置的原生 ActorTemplate 在启动时经控制面读取，核对资源、UID 和启动规格，再发布租户内 SDK template ID/alias 映射。目录保存原生 UID、去除服务端 metadata/status 的确定性规格摘要、资源和 envd profile，以及原生创建/更新时间。目录与引用在同一 PostgreSQL 事务内提交，按租户串行处理发布；同 ID 的不同规格拒绝，alias 与 ID 共用引用名字空间，不能抢占其他模板的名称。重复发布保持原记录，迁移校验失败拒绝启动。

SDK Create 改为从持久目录查找 ID/alias；不再以启动时的内存列表作为运行时目录。仍保留内存路径供现有单元 fixture 使用。创建前核对当前控制面原生 UID 和规格摘要，拒绝删除重建同名原生模板或修改已发布规格，不在此情况下启动新实例。CPU/内存 profile 在目录初次发布及创建时都与原生机器资源核对。创建 intent 继续冻结完整原生模板，后续后台执行不重新读取模板目录。

已接通 GET /templates、GET /v2/templates、GET /templates/aliases/{alias} 和 GET /templates/{templateID}。分页按持久创建时间和 ID 降序，游标绑定租户；零 limit 返回空数组，未指定 limit 保留原版全量语义。GET detail 返回已预置 artifact 的一条 imported build，buildID 使用被冻结的原生 ActorTemplate UID，原生时间作为导入 artifact 的时间；templateID 是兼容层 ID。响应 private/public=false，目录查询不会跨租户。当前 buildCount=1、spawnCount=0、lastSpawnedAt=null，与原版记录转换中固定计数的行为对应，未冒充真实运行统计。无论 HTTP 还是后台创建，不接受调用方通过请求自行登记原生 UID/规格。

这是已预置模板的持久发布和读接口，不是动态构建闭环。V3 构建申请、内部 builder Worker、BuildKit/OCI 导入、持久步骤/日志/缓存、构建 cancel、snapshot refresh、删除/outbox/GC、alias 最新版本切换仍需要编码。不能把读接口或一条 imported build 称为已完成 Dockerfile/SDK BuildKit 构建。卷和共享层/catalog 完整生命周期、Actor 设备引用与节点故障恢复以及成套普通 K8S 交付仍有缺口，整体未完成。

验证：真实临时 PostgreSQL race 验证目录重启、幂等发布、ID/alias 冲突事务回滚、租户隔离、相同时间分页和修改 migration checksum 拒绝；bridge 全包 race（mandatory PostgreSQL）通过，新增模板 detail/空游标和零 limit 的最终 HTTP/creation/main 回归通过。bridge Linux amd64 构建及 Go vet 通过。没有执行实际 template artifact 导入、动态构建、原版/融合版 SDK 对照、Rust/VM/S3/K8S 验证，不能计为通过。


### 2026-10-09：模板构建持久状态与发布事务

本次增加 bridge 模板构建任务和日志的独立、带摘要校验的 v1 数据库迁移；启动时自动执行。构建输入在分配前冻结，租户内请求键幂等，改变输入会冲突；数值不经过浮点转换，持久输入读取时复核摘要。任务只从 queued 并发领取一次，以 owner、generation、状态和数据库时间租约校验推进及续租；过期任务转 uncertain 并保留 Actor 身份，不能重新领取或自动重跑。排队任务可直接取消，运行中取消仅记录请求，不能据此假定 VM 已停止。日志按事件键幂等、顺序持久化，限制单条 16 KiB、总计 8 MiB / 32768 条，旧执行者不能写入。

模板目录读取新增 profile 摘要及租户 / ID 一致性校验。产物发布支持对别名原目标进行比较后原子替换，旧模板 ID 保持不变，迟到构建不能覆盖新版本；失败时新目录记录和别名修改一起回滚。

**实现边界：** 以上是构建编排持久化和发布事务的实现；尚未接通 SDK 构建写接口、内部 builder Worker 分配、已分配 Actor 中的步骤执行、快照产物发布、构建取消后的停止确认或 uncertain 任务对账。不能将这些数据库方法计为完整 F08，也没有启动后台任务假装完成构建。卷管理、共享层生命周期引用与 GC 接线、节点故障恢复、完整安装运维交付仍有功能编码缺口，除真实设备 / SDK / 集群验证以外继续保留待实现状态。

验证：bridge 全模块 Go race 测试（实际临时 PostgreSQL，强制数据库测试）通过；覆盖并发领取、重复日志、不可变输入、损坏目录 / 输入、跨租户读取、租约过期、发布回滚与迟到构建冲突。Linux amd64 bridge 构建、Go vet 与 diff 检查通过；完整回归记录为 `/private/tmp/aenv-build-jobs-regression.log`，新增测试记录为 `/private/tmp/aenv-build-jobs-final-tests.log`。Rust、VM 和 K8S 验收仍未执行。


### 2026-10-09 剩余方案实施：cold、保活、批量指标与显式快照

本次补齐 POST /sandboxes/cold、POST /sandboxes/{id}/refreshes、GET /sandboxes/metrics、POST /sandboxes/{id}/snapshots、GET /snapshots 与 GET /snapshots/{id} 的实际入口和服务调用链。

- refresh 是保活：缺省 duration 使用配置默认值 15 秒，显式 0 保留零值；只延长当前运行实例的截止时间，不恢复暂停实例，不缩短已有期限。事务检查到期、暂停和恢复中的持有状态。
- 批量 metrics 校验最多 100 个沙箱 UUID，绑定租户和当前 Actor UID、executor instance、assignment generation；无样本或非运行实例省略，不返回旧分配的数据。
- cold 使用预配置的用户模板执行配置及 Worker selector。独立镜像解析工具将 Linux amd64 OCI 镜像及附加驱动器镜像冻结为摘要；创建意图冻结 CPU、内存、磁盘、启动参数、挂载点与子路径。重复 key 不重新解析可变 tag。Worker 转交 Rust 的镜像驱动器解析及启动链，返回实际根盘容量；OCI 环境、工作目录和用户被保留。该附加镜像驱动器能力不等于持久卷管理已经完成。
- 显式快照使用原生在线 Capture，源 Actor 继续运行。metadata v9 新增捕获任务、快照记录和名称预留，历史迁移未修改。发送前冻结 Actor UID、generation、模板 UID 和目标 Tag 名；未知结果保留并重试同一操作。快照记录和成功回执原子提交，捕获期间保护源实例免受到期、暂停、删除及扩展修改竞态影响。查询与分页绑定租户、过滤条件及不可变快照 ID。

验证：bridge 全包 race 测试（必须连接真实临时 PostgreSQL）通过；新增 HTTP 捕获/分页/跨租户测试、冷启动请求冻结与重试测试、快照未知结果重试和 v8→v9 数据迁移测试通过。Go vet 与 bridge Linux amd64 构建通过；Substrate 镜像解析、AgentENV 校验与 atelet 冷启动转换定向 race 测试通过。协议使用官方脚本重新生成。Rust 只完成格式检查，未执行 Cargo 编译；真实 OCI/Firecracker、S3、SDK 对照及 K8S 未执行。

剩余编码边界：快照恢复创建的兼容 ID 解析、控制面 catalog reserve/owner/pin 接线、共享只读层和 GC、实际模板构建协调器与执行链、持久卷管理和带卷 fork、设备角色对账/明确 fencing 证明、多 Actor 锁拆分以及成套 K8S 安装运维仍未完成。本节不能作为 F01–F11 全部完成的声明。
