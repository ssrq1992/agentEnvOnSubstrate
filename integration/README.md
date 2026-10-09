# AgentENV / Substrate 融合实施状态

更新：2026-10-09。**当前为未完成的开发工作区，不是可安装的完整融合发行版。**

已开始实现用户确认的完整计划，没有把最终范围改成 M1。当前缺口包含尚未编写和接线的软件，不能描述为“实现完成，仅待集群验证”。不要将新增 `agentenv` 枚举理解为后端已经可用。

## 基线与工具

| 项目 | 固定基线/工具 | 本次结果 |
|---|---|---|
| Substrate | v0.4.0 / `756c2a53741121e728f4cc3066c8a19e575b4919` | 工作区已修改，未提交 |
| AgentENV | v0.2.3 / `6cccaa7842bd5be2051111d4f74d9e37aa721244` | 工作区已修改，未提交 |
| Go | 仓库指定的 1.27.0 | 可使用，定向测试与生成成功 |
| Rust | `rust-toolchain.toml` 固定为 1.99.0 | 已校验下载固定版本 rustc/rustfmt，修改文件格式检查通过；cargo/Linux 构建未执行 |
| Protobuf | Substrate 锁定的 protoc 25.3 及 Go 插件 | 完整生成成功；Rust 由 build.rs 生成，尚未执行 |
| SDK/运行资产/镜像套件 | SDK harness 已固定 E2B 2.53.1、CLI 2.21.1、Node 22/Python 3.12；运行资产仍需发布套件 | 锁文件安装和导入已通过；原版/融合版 SDK 对照与运行资产发布未验收 |

## 当前源码交付

| 组件 | 已写入的实现 | 构建/测试证据 | 未完成部分 |
|---|---|---|---|
| class 接入 | public/internal proto、CRD、转换、校验生成、指标 class、Worker 权限和专用节点选择、运行资产规则 | Go 生成成功；API 校验和 Pod 形态定向测试通过 | 启动/恢复链已接线，跨语言执行和完整资产验证仍未完成 |
| 私有协议 | canonical proto、Go/Rust 生成入口、分配代次/实例身份、操作日志、在线捕获和策略 ACK | Go 生成及 race 测试通过；分配代次已跑真实 PG 回归 | Rust 构建及跨语言互操作仍待验证 |
| Go 执行客户端 | UDS 权限、fence、operation ID/digest、UNKNOWN/Inspect、native adapter、进程监管、网络所有权、atunnel | 客户端、journal、adapter 与 atelet race 测试通过 | 逐 Actor cgroup 及 host stats 已接通；设备会话/逐设备账本与预算已接通；Actor 设备引用与完整故障对账、真实节点隔离与指标未验收 |
| Rust embedded | 独立 executor、持久日志、资源计数、Start/Restore/Capture/Stop/策略/扩展/Stats、drain | 已写入并格式化，**未编译、未运行** | 逐 Actor cgroup 子进程入组与恢复注入已接通；逐 kernel device 账本与预算已接通；Actor 设备引用、节点对账及恢复身份验收仍未完成 |
| FULL 包 | 内存/rootfs/附加驱动器/tools 展平、摘要及路径重建；atelet S3/GCS 上传恢复链 | Go 上传/捕获测试通过；Rust FULL 实现未编译 | Linux 完整资产闭包及跨 Worker 文件/内存恢复验收未完成 |
| 网络策略 | 外部 netns/TAP；公共类型化 EgressPolicy、持久 revision、私有 ACK、重试对账及启动/恢复策略注入 | Go 私有链路 race、真实 PG 策略事务定向测试通过 | SDK PUT network 已接入并有 Go 测试；预期 revision 的持久准备/并发防覆盖已有测试；Rust 编译和真实网络/域名语义验收未完成 |
| 通用证书 | TokenReview/SPIFFE、init/轮换 agent、bootstrap、issuer/RBAC 清单、五类工作负载清单转换 | 证书/清单/bootstrap race 测试与 Linux Go 交叉构建通过 | 集群签发与轮换未验收；issuer 自身证书及根轮换还需运营整合 |
| 普通信任根 | ConfigMap/file provider；替换实验投影，目录挂载保留轮换可见性 | Worker/ConfigMap 与清单转换测试通过 | 原默认安装器仍走实验 API；数据库、OIDC、对象存储及节点配置未组成完整普通 K8S 安装器 |
| bridge/catalog | PG catalog/元数据/expiry，租户认证、Gateway mTLS；SDK 创建/列表/详情/连接、数据代理、删除、timeout、network、metrics、持久 pause、批量 fork 和扩展 GET/PATCH | bridge 全包 race、真实 PG v1/v7→v8 迁移与操作事务测试通过 | 模板构建/卷 API、catalog 与控制面生命周期提交的完整接线未完成；真实 SDK 对照未验收 |
| M3 控制面工作流 | 原生公共在线 capture、源 Tag UID 固定的子项创建、bridge 批量 fork 和公共扩展参数更新已接通 | Go 捕获、重试、源实例保留测试通过 | 公共 capture/fork、builder、卷写者/版本及 refresh 未完成；超时 HTTP 和 expiry 已实现，完整生命周期联动待接线 |

Rust 执行端没有启动旧 Orchestrator、NetworkManager 或预热池。多个 Actor 共用一个执行端，但当前生命周期变更由一个全局锁串行处理；默认 `max_actors=1`，有 CPU/内存计数准入。这不等于多 Actor 隔离验收已经完成。

当前安全边界采取保守失败方式：执行端重启若发现 `live/` 或 `devices/` 日志，拒绝激活，要求先隔离旧 Worker。它避免“旧 VM 可能仍活着却启动替身”，但不是计划要求的完整重启对账与故障恢复。操作日志不是控制面 assignment generation 的替代品。embedded 停止已传播设备清理错误并阻止未确认状态下的盲目重试；已接入持久设备会话及逐 kernel device 账本，仍缺 Actor 设备引用与节点隔离恢复闭环，不能据此声称故障恢复验收完成。

## 私有接口约束

- `Execute` 使用类型化 Command oneof：Start、Restore、Capture、Stop、UpdatePolicy、UpdateExtensions。`UpdateExtensions.json` 表示传给既有 extension patch hook 的补丁；成功后保存 hook 批准的完整参数。
- fence 包含协议版本、规范小写 Actor UUID、Worker Pod UID、epoch、Rust 进程实例 UUID、assignment generation。上游必须从权威分配记录获取这些值，不能允许兼容 API 用户指定。
- operation ID 必须由上游先持久化。digest 为 Command 确定性 protobuf 编码的 SHA-256。重试保留 ID/内容；NO_EFFECT 和 EFFECT_UNKNOWN 分开，未知结果需要 Inspect/Reconcile。
- Restore 校验机器规格、兼容域、资产摘要、网络签名和包摘要，失败不得转为冷启动。运行资产路径、namespace 和本地快照路径是 Worker 私有路径，不是用户 API 字段。
- Capture 的 `continue_running=true` 使用现有 Firecracker 一致捕获并原地恢复；false 捕获后停止 VM。它仅是执行端能力，尚不是已接入 Actor lease 的公共工作流。
- Stats 当前是 guest 指标，不能冒充包含 VMM 开销的 cgroup 指标。

## 权限和配置

`WorkerPool.spec.podIdentityIssuer` 新增 `endpoint`、`agentImage`、`trustConfigMap`。ConfigMap 需要 `issuer-ca.crt`、`podidentity-ca.crt`、`servicedns-ca.crt`；根证书文件投射不能用不会更新的 subPath。签发服务只接受 allowlist 中的 `namespace/serviceAccount`，调用方使用 audience 为 `podidentity.ate.dev` 的 Pod-bound token。

签发服务需要创建 `authentication.k8s.io/tokenreviews`，读取 Pod、ServiceAccount、Node，列出调用方 namespace 的 Service。私有签发池仅挂载到签发服务，不能放入公共根 ConfigMap。HTTPS 服务自身的初始证书仍需安装阶段提供。

新增 atelet 参数为 `--egress-trust-bundle-file`；新增 controller 参数为 `--configmap-trust-provider`。不配置它们时保留原有 CTB 行为。Worker 配置通用证书时会真正替换 PodCertificate/CTB 投射，并用 init 获取第一张证书、普通 sidecar 续签。

AgentENV Worker 使用 `privileged=true` 和专用节点标签 `ate.dev/agentenv-capable=true`，容忍 `ate.dev/agentenv=true:NoSchedule` 污点，挂载主机 `/dev`。这是尚待收紧和真实验证的设备权限配置，不能声称最小权限；gVisor/microvm 保持原有非特权模式。没有新增默认 AgentENV WorkerPool 来自动启用尚未接通的后端。

## 可复现检查入口

在具备工具链及原版构建依赖的 Linux amd64 环境，从本目录执行：

```sh
bash verify-source.sh
python3 check-node.py
```

`verify-source.sh` 是组件源码检查，缺少工具或错误版本时返回非零；它不是完整安装或 F01–F11 验收。`check-node.py` 只读取设备和 cgroup 条件，不创建 VM；需要在拟运行 Worker 的权限上下文执行。预检通过仍不能证明 ublk 实际 I/O、隔离或网络正确。两个脚本都不把缺设备记为通过，相关失败分支有单元测试。

本次实际检查结果：

- 完整 `hack/update/codegen.sh`：通过，包含官方生成器生成的 protobuf/CRD/校验代码。
- Go race：`internal/aenvexecutor`、`internal/podidentityissuer`、`cmd/atelet/internal/trustbundle`、`cmd/ateapi/internal/apivalidation` 通过。
- 上述组件以及 atelet/controller/两个证书命令的 `go vet`：通过。
- Go `cmd/atelet`、`cmd/atecontroller`、`internal/ateattr`、`internal/ateomstats` 的本地测试通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/atelet ./cmd/atecontroller ./cmd/podidentityissuer ./cmd/podidentityagent`：交叉编译通过；没有构建或运行融合 Worker 镜像。
- Worker Pod/原后端权限/ConfigMap provider 三项测试按文件列表实际执行并通过（含 race）；没有把 controller `-short` 的整包跳过计入通过。
- controller/CRD 的完整 envtest 测试：环境工具准备/锁等待超过 11 分钟，失败；未计入通过。
- `make verify`：未通过。macOS 编译原有 `internal/ateomnet/dns` 时缺少 Linux `netns.Handle/Do`；之后终止剩余本轮测试进程。
- 指标注册表检查：缺少 Weaver/Docker，未执行。仓库 gofmt 验证器要求干净工作树，拒绝运行；另行检查本次 Go 文件格式及 diff。
- Rust fmt/check/test：本机没有可用 Rust 工具链，未执行。新增 Rust 测试包含日志幂等、旧 generation、进程实例更新、遗留 live 拒绝启动、drain、包路径与平台 CIDR 检查，不能把写入测试视为通过。
- 预检 Python 单元测试：2 项通过；本机真实预检退出 1（缺少 Linux/KVM/ublk 等条件），符合失败预期。
- 真实 VM、K8S 安装、SDK 对照、性能：均未执行。

## F01–F11 验收账本

全部仍是 **未验收**。下表是下一步必须编写并运行的对照场景，不是已经通过的自动化用例。

| ID | 必需验收场景 | 当前阻塞软件 |
|---|---|---|
| F01 | cold/template create → get/list → connect → delete；ID/错误/状态等价 | native 执行链真实验证、故障对账、bridge API |
| F02 | argv/env/user/cwd、退出码、stdin/stdout/stderr、PTY、取消/断连 | ingress/argv 真实验证、bridge、完整启动描述 |
| F03 | 文件读写/上传下载、权限/越界、恢复后大文件摘要一致 | bridge/端口路由、持久恢复链 |
| F04 | host/header/流式端口访问、鉴权、跨租户、builder 隐藏 | atunnel 接入、bridge 授权 |
| F05 | Default/IP/CIDR/通配符、平台禁区、更新失败保持旧策略 | typed EgressPolicy CRUD/下发确认、Go 网络管理 |
| F06 | durable suspend 后删除源 Worker，另一节点恢复文件和内存；失败不得冷启动 | 完整资产闭包、快照提交、恢复身份、catalog |
| F07 | 源继续运行，多个子实例独立身份/token/可写盘；部分失败回收 | lease 下 capture API、控制面 fork workflow |
| F08 | build/import/alias/list/delete、状态/日志持久化、内部 builder 隔离 | builder Worker、bridge PG、模板/catalog |
| F09 | 设备槽/挂载、写者排他、快照卷版本、恢复一致性 | 卷 descriptor/slot 接线、版本与 owner 事务 |
| F10 | timeout 延长/到期、实际资源指标、扩展 start/stop/patch | bridge timeout、guest metrics/历史采样、cgroup、完整扩展对照 |
| F11 | 固定 Python/TS/aenv 版本，原版和融合版执行同一套用例 | 完整兼容层与 F01–F11 对照 runner（SDK 版本及基础 smoke runner 已固定） |

下一实现顺序仍是原计划：先完成 native adapter + 控制面 fencing + M1 持久闭环，再接 bridge/catalog 和有界多 Actor，最后补齐 M3 和普通 K8S 安装验收。NodeBroker/P2P/统计超卖仍在本轮之外；不得借此排除 fork、模板构建、卷和完整主要 SDK 功能。


## 继续实施：分配代次与 Go 操作日志

本次新增的代码仍属于执行链基础，不能视为 M1 或最终融合实现完成。

- PostgreSQL 增加 `assignment_generation` sequence；首次绑定由数据库分配代次，同一绑定重试保留原代次，释放后重新绑定获得新代次。协议已重新生成。
- 调度与绑定要求 Worker 当前 epoch 已完成对账；旧 epoch 的绑定不可重写成新 epoch。恢复执行前，AgentENV 的 Actor 分配代次必须与持久 Worker claim 一致。
- Go executor client 新增持久操作日志：调用前同步写入原始 fence、命令及摘要；完成结果原子落盘；独占进程锁与 Actor 锁限制并发。重启读取已完成结果，不重复发送；断线或结果落盘失败保留“效果未知”，不授权清理或释放容量。Inspect 可确认原操作结果，缺失结果仍保持未知。
- `go test -race ./internal/aenvexecutor` 实际通过，覆盖重启、身份/载荷冲突、断线重试、写盘失败、查询确认、损坏日志与符号链接拒绝。调度 epoch 表测试与迁移策略测试实际通过。
- PostgreSQL store contract 已增加代次断言，但本机缺少 Docker，数据库行为尚未实测；相关跳过不计通过。上述实例注册、生命周期传递和 native adapter 日志接入在后续本轮修改中已写入；完整数据库和真实执行链尚未验证。


## 当前继续实施检查点

- 新增 `substrate/cmd/ateom-agentenv`：从已认证 Worker 查询 epoch，启动并监管唯一 Rust executor，握手后注册实例 ID/资源预算，再开放 readiness；Rust 退出导致 Worker 退出，不在同一 epoch 内自动重启。Linux amd64 交叉编译通过。
- Worker bootstrap 不再依赖 readiness 循环：AgentENV 的运行中 Pod 可先创建注册身份；调度必须同时满足 observed epoch、registered epoch 与非空 executor ID。原后端 readiness 条件保留。WorkerPool 新增 `spec.agentenv.maxActors`，默认 1，可显式配置多 Actor。
- 新增 Go 网络管理和适配服务：独立 netns/TAP/veth/NAT、持久所有权、停止记录、失败资源保留；完整身份先于资源操作验证。已确认停止后才能回收，已确认创建的部分拓扑可以清理，创建结果未知的对象不会擅自删除。
- atelet 新增类型化启动准备，跳过 AgentENV 的 OCI 解包/目录重置。随机 envd 凭据与请求摘要同步落盘并在重试中复用；准备记录不进入快照。恢复传递相同机器规格，暂要求 FULL；原有对象传输已复用；基线已支持 `ATE_STORAGE_BACKEND=s3`，S3 集群验收尚未执行。
- Go FULL 包搬运拒绝非普通文件、路径越界、重试覆盖不同内容；Rust 冷启动 argv 已接原有 envd start_process，恢复不会重新执行 argv。Rust 改动仍未编译。
- Gateway `mode=substrate` 跳过旧 Scheduler 连接，通过 TLS 1.3/mTLS 转发到 `bridge_url`，按新连接读取轮换后的证书/根。保持 SDK Host/编码路径及凭据头，清除伪造内部身份头。该模式需要真正的 bridge API 服务，当前 bridge 已有删除/续期路由和启动入口，其余 SDK 路由尚未全部接入。
- `AgentENV/services/aenv-api-bridge/internal/catalog` 实现独立 schema、校验迁移摘要、内容寻址键、操作引用、持久 owner、带完整执行 fence 的 pin、释放 outbox 和延迟 GC。owner/runtime 停止记录防止旧请求恢复引用。GC 与引用保留使用同一层锁；上传确认按 operation 记录，避免“对象删除成功但数据库提交失败”后直接复用缺失对象。
- 新增 Worker Docker 构建入口 `build-worker.sh` / `Dockerfile.worker`；AgentENV 原 runtime 镜像构建增加 aenv-executor，保留 standalone 入口。Docker 缺失，镜像未构建。

新增证据：native adapter/network/supervisor、executor journal、atelet 启动准备、注册/调度/身份传递、Worker bootstrap/Pod 参数与 Gateway 定向测试实际通过；相关 Go vet 与 Linux Go 交叉编译通过。catalog 纯逻辑测试通过，数据库测试因没有 PostgreSQL DSN 未执行。已实际验证 `REQUIRE_DOCKER=true` 和 `REQUIRE_CATALOG_DATABASE=true` 在依赖缺失时返回失败，未把这些预期失败记为数据库通过。Gateway 全包回归已完成，见下方后续记录。F01–F11 全部仍未验收。

### 2026-10-09 继续实施记录

- `aenv-api-bridge/internal/metadata` 已增加独立 schema 迁移、外部 ID/Actor 固定映射、请求摘要幂等记录、确认结果缓存和超时 revision 更新。创建意图先持久化，传输结果未知时保留 pending。该模块已接入删除/续期 HTTP 路由和后台超时回收，创建、连接等路由仍未全部接入，不能据此声称 SDK 生命周期已经贯通。
- catalog 与 metadata 已在自身 Go module 中构建并通过本地逻辑单元测试；PostgreSQL 合约测试因缺少 `AENV_CATALOG_TEST_DSN` 未执行。强制数据库分支已实测失败，Linux 验证入口强制运行数据库测试。
- 控制面已禁止通过普通 crash、Worker epoch 递增、失效调度约束或删除 Worker 记录自动释放未确认的 AgentENV 实例。孤立分配同样保留。正常明确停止后的释放流程保留；节点隔离证明与自动恢复闭环仍待实现，不能将 fail-closed 等同于故障恢复完成。
- atelet 的 AgentENV Terminate 重试不再依赖已经被清理的运行资产，且执行端 NotFound 不再被解释为停止成功。相关测试在允许本机 UDS 的环境通过。
- embedded 停止会传播 Firecracker wait/kill、设备释放及预取任务清理失败；失败后不再盲目重试可复用的设备 ID。新增 Rust 回归用例尚未编译运行，完整设备持久账本仍待实现。
- Worker guest 预算扣除固定开销和 `maxActors` 份单 Actor 开销；默认每 Actor 预留 250m CPU、128 MiB，可通过 Worker 参数调整。预留值是部署起始配置，尚无真实节点测量背书。
- 私有 Stats 客户端校验完整分配身份与测量有效性。envd 的 CPU 百分比没有冒充 Substrate 的累计 CPU 时间；公共统计接入仍待补齐。

- Gateway 的 `go test -race ./gateway/... ./shared/config` 已完成并通过；不再属于依赖下载中的检查。
- 固定版本 Rust rustfmt 已经校验下载产物 SHA256，并格式化全部修改的 Rust 文件；此项不等于 Rust 类型检查或 Linux 构建通过。
- 存储核对纠正：v0.4.0 原有 `pkg/objectstorage` 与 `internal/objectstore` 已提供 S3 上传、下载、删除和拷贝，atelet/ateapi 均可通过 `ATE_STORAGE_BACKEND=s3` 选择。新增工作是 catalog 内容寻址层的校验和引用衔接，不重写已有 provider。

### 2026-10-09 服务入口与凭据边界

- 新增 `aenv-api-bridge/cmd/aenv-api-bridge`，启动时验证配置、mTLS 材料和 PostgreSQL 迁移，再启动 HTTP API 与超时回收循环；退出时关闭请求和回收任务。当时实际 HTTP 路由只有删除和续期；后续已增加 PUT network，其他 SDK 路由仍未完成。
- 超时回收先锁定 sandbox 行并持久化 expiry claim，再向 Substrate 发删除请求。续期通过相同行锁核对 revision 和 expiry claim；已开始且结果未知的删除不接受续期。删除确认后原子提交元数据 tombstone；请求超时不等于删除成功。数据库并发合约尚待真实 PostgreSQL 执行。
- bridge 已有基于 Gateway SPIFFE/mTLS 和 SDK API key 摘要的租户认证、每请求读取轮换 OIDC token 的 Substrate 客户端、S3 内容摘要校验、上传前保留 catalog 引用的 publisher。完整快照提交工作流尚未连接 publisher。
- Substrate 增加 `ConnectActor(actor, uid)`，在 Actor lease 下核对 RUNNING、Worker 完整分配身份，再向 atelet 读取当前私有 envd token；非 AgentENV、旧 UID/epoch/instance/generation 均拒绝。bridge control client 已接入，但 SDK connect 路由尚未完成。凭据不写入持久请求回执或快照。
- 授权注册表补齐 Suspend/Pause/Resume/Revert、Actor EgressPolicy CRUD 和 ConnectActor。凭据读取要求 `can_update`，策略读取要求 `can_get`，策略修改和生命周期要求 `can_update`。普通集群部署必须开启控制面授权；新增检查沿用现有 enforcement 开关。
- FULL 包现在在 Rust 源码中包含只读 tools 镜像和摘要，恢复从包内重建路径；已 rustfmt，尚未 Rust 编译或实际 ublk 读写验证。
- Python 3.12 的 SDK 锁定安装成功；Node 22 的 npm ci 和 E2B 导入成功。Node 使用 `NODE_EXTRA_CA_CERTS` 加载本机 CA，未关闭 TLS 验证。Python/TypeScript smoke 对照 runner 已新增，但无后端地址，未执行功能对照，也不代表 F01–F11 全覆盖。
- 本地 Go：bridge 逻辑/HTTP/证书轮换测试、控制面/atelet/授权测试已通过；数据库测试缺 DSN 跳过，不能计为数据库通过。Rust、容器、S3 服务端、Linux 设备、真实 SDK 和 K8S 验证仍待执行。

### 2026-10-09 实际数据库验证及在线捕获通路

- 从 PostgreSQL 官方 18.0 源码构建了一次性本地测试实例，校验 SHA256 为 `0d5b903b1e5fe361bca7aa9507519933773eb34266b1357c4e7780fdee6d6078`。仅使用私有 Unix socket；不是 Kubernetes 或生产部署验证。
- bridge 全包 `go test -race -count=1 ./...` 在设置 `AENV_CATALOG_TEST_DSN` 和 `REQUIRE_CATALOG_DATABASE=true` 后实际通过，包含 catalog GC/引用合约、元数据幂等、过期删除、20 轮续期/删除并发竞争。上述数据库模块不再属于“只写了测试/因 DSN 缺失跳过”；S3 服务端、VM 与集群测试仍未运行。
- Substrate 测试新增 `ATE_TEST_POSTGRES_DSN`：只在指定服务器新建随机名的独立测试数据库，清理只删除这些库，不迁移或清空 DSN 指向的管理数据库。未配置时保留原 Docker 路径。授权全包在实际 PostgreSQL/OpenFGA 数据层通过；控制面数据库回归随后实际通过（110.7 秒），见下方兼容修正。
- 新增内部 `atelet.Capture` → `ateom.CaptureWorkload` → executor `Capture(continue_running=true)`，保留源入口、网络、运行记录和旧 checkpoint；每 operation 使用独立目录，并先持久化完整请求摘要，禁止重试改写目标 URI。Go UDS/适配器测试通过。公共 `CaptureActorSnapshot` 的 lease/持久提交与 fork 子 Actor 工作流仍未完成；暂存导出目录回收仍需衔接控制面确认。
- 运行凭据、私有 LaunchSpec token 和环境变量已标记 protobuf `debug_redact`；脱敏与原始请求不变测试通过。bridge 控制面信任根随 gRPC 重连加载，重叠根、移除旧根和主机名拒绝测试通过。
- atelet 全包曾暴露原预热测试的临时目录清理竞争：共享镜像 pull 会在调用方取消后继续写入。测试现在先等待共享 pull；定向连续 10 次及 atelet 全包回归通过。

复现实数据库测试（DSN 必须指向专用测试服务器，账号允许 CREATE DATABASE）：

```sh
# Substrate：各测试创建并删除独立库。
ATE_TEST_POSTGRES_DSN='host=... user=... dbname=postgres sslmode=verify-full' REQUIRE_DOCKER=true go test -race ./cmd/ateapi/internal/authz ./cmd/ateapi/internal/controlapi
# AgentENV/services/aenv-api-bridge：同样使用独立测试库。
AENV_CATALOG_TEST_DSN='host=... user=... dbname=postgres sslmode=verify-full' REQUIRE_CATALOG_DATABASE=true go test -race ./...
```

后续验证与修正：

- 控制面在真实数据库回归中发现 4 个原 gVisor epoch/rebind 行为回归；已将新增的对账/注册门控限制到 AgentENV，保留原后端既有行为。完整 controlapi 和 scheduling race 回归通过；新增 AgentENV 数据库测试确认门控仍拒绝未对账、未注册和旧执行实例重新绑定，且旧 claim 不被覆写。
- 授权测试原有 Docker 缺失分支现在遵守 `dockerenv.Required()`，已在缺 Docker 且 `REQUIRE_DOCKER=true` 时观察到预期失败，避免 CI 静默跳过。
- Rust 恢复源码改为保留未显式覆盖的网络策略与扩展参数，环境变量在快照设置上合并；已补测试并 rustfmt，仍未 Rust 类型检查或执行。


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

### 2026-10-09：SDK 运行中策略更新与控制面前置条件

- bridge 增加原版 `PUT /sandboxes/{sandboxID}/network`：`allowOut`、`denyOut`、`allow_internet_access`；省略字段清空规则，省略布尔值使用 Default。拒绝 null、类型错误、多 JSON 文档和过大请求。
- Create/Update/DeleteActorEgressPolicy 增加可选 `AgentENVPolicyPreconditions`，在 Actor lease 内检查 UID 和 RUNNING。非 native 后端拒绝此条件，避免静默忽略。SDK 更新要求 UID 与 RUNNING；原生控制面仍可给 suspended Actor 暂存策略。
- bridge 仅在返回的 policy UID/version 与 applied revision 对应且带 executor 身份时确认成功。已完成的幂等请求直接返回；未确认执行保留 pending。同一策略重试不增加版本或运行 revision。
- 已通过 bridge control/httpapi race、vet、bridge 构建，以及真实 PostgreSQL 的策略 CRUD/身份/暂停前置条件与重试定向 race 测试。未运行 Linux 策略执行或 SDK 服务端对照。
- 后续已补持久准备与乐观 revision 条件：执行前将读取到的预期 revision 写入 request.result；并发重试采用先提交的准备值。控制面先修复策略行与 delivery 的崩溃窗口，再比较预期 revision，拒绝过期不同策略；相同目标允许确认。删除后重建也不能复用旧 revision。真实 PG 的 12 并发准备测试、控制面旧请求覆盖/删除重建回归通过，bridge 全包 race 通过。F05 仍待真实执行及 SDK 对照。

### 2026-10-09：分配身份约束的 guest metrics 读取

新增公共 `GetActorGuestMetrics(actor, uid)`，按 Actor 读取权限授权；在生命周期 lease 下验证 RUNNING、Worker Pod UID、epoch、executor 实例与 assignment generation。私有 `ReadGuestStats` 经 atelet 本地 preparation 核对、Worker Actor 锁和 executor Stats 客户端返回 envd 样本。返回含采样时间、CPU 核数及百分比、guest 内存/缓存与磁盘字节数；不转写成累计 CPU 时间或宿主机 cgroup 用量。

Go 样本校验拒绝旧分配、缺失时间/CPU 核数、NaN/Inf、负 CPU 使用和超出总量的内存/磁盘。已补 adapter、UDS、真实 PostgreSQL 控制面与授权测试。桥接客户端具备租户绑定的 GuestMetrics 调用，后续已接入 SDK 历史 metrics 路由与后台采样，见下一条记录。Rust 已增加原始样本 CPU 核数并格式化，尚未 Linux Rust 编译。

### 2026-10-09：SDK 历史指标与后台采样

- bridge 新增 `GET /sandboxes/{sandboxID}/metrics`，支持非负整数 start/end（秒、闭区间）、默认全部保留样本、时间升序以及无样本时返回 `[]`。身份来自已认证租户和 metadata 的固定 Actor UID。
- 后台每 15 秒尝试采样，单轮 12 秒预算、最多 8 个并发 RPC、每次最多 2 秒；分页失败保留游标，停止/暂停或采样失败不写伪造零值。忙碌集群可能出现采样空档，不能保证固定间隔都成功。
- 新增有摘要校验的独立 metrics migration；PostgreSQL 按 tenant/external ID/Actor UID/executor 实例/generation/采样时间去重，采样写入前再次核对 metadata 身份和 tombstone。保留 1 小时，读取即时排除过期数据，删除每轮最多 10000 行。多个 bridge 副本共享历史；与原版节点本地、重启丢失历史相比，历史现在可跨 bridge 重启保留，该差异需列入 F10 对照报告。
- 真实 PostgreSQL 的迁移、租户/旧 Actor 隔离、去重、闭区间、排序和保留期测试通过；HTTP 参数校验与 collector 跳过暂停/错误租户、分页失败恢复的 Go race 测试通过。尚未在真实 envd/VM 上运行 SDK metrics 对照。


本轮最终检查记录：bridge 全包 `go test -race ./...`（强制真实 PostgreSQL）、`go vet ./...`、Linux amd64 bridge 构建通过；Substrate guest metrics 相关 adapter/executor/validation race、控制面/atelet 路径测试、授权测试、相关 go vet 和 ateapi/atelet/ateom-agentenv Linux amd64 构建通过。策略改动的控制面/validation/authz 全包 race 通过，随后新增未来 revision 拒绝测试定向通过。两仓库 tracked diff 的空白检查通过。Rust 只执行 rustfmt，未执行 cargo check/test；未执行真实 SDK、S3、VM 或 K8S 验收。整体软件及 F01–F11 仍未完成。

### 2026-10-09：SDK 持久暂停及到期回收互斥

- 新增 `POST /sandboxes/{sandboxID}/pause`，映射 Substrate 持久 Suspend；请求绑定 Actor UID 和原 assignment generation，旧请求不能暂停已经恢复的新分配。仅收到 SUSPENDED 且具备持久快照 URI 的确认才返回 204。
- metadata v2 迁移保留旧迁移摘要并新增 suspend_intents；先锁 sandbox 行，再保留暂停意图及定时器 hold。到期回收锁定行后再次读取 hold，避免查询快照较旧导致暂停与删除同时获准。已决定到期删除时，暂停返回冲突；暂停 pending/已确认时，不再到期删除或重新设置 TTL。
- bridge 重启后的后台重试沿用同一个 generation；重复暂停加入同一个意图。失败/未知结果不标记成功。恢复接口及恢复后移除 hold、重新启用 TTL 的原子提交已接通（见后续记录）；原生路由触发自动 Resume 与 SDK autoResume 策略仍需继续约束。
- 已通过真实 PostgreSQL 的迁移、暂停/到期互斥、20 轮并发竞争、恢复旧 schema、重试和晚到确认防护测试；bridge 全包 race、vet 与 Linux 构建通过；Substrate controlapi/apivalidation/authz 全包 race 通过。第一次数据库运行因重启使用错误 socket 失败，修正为私有 socket 后重跑通过，未将失败记作通过。真实 VM 持久暂停与 SDK 对照未执行。


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
