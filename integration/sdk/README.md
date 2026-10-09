# 固定版本 SDK 对照检查

此入口复用原版 Python/TypeScript 兼容脚本，逐个运行原版与融合版，检查生命周期、命令、模板及既有卷用例。它不是完整 F01–F11 验收，也不覆盖真实 VM 内存跨节点恢复、故障注入和集群升级。

版本见 `versions.json`。npm 依赖由 lockfile v3 固定，Python 依赖由带哈希的 universal lock 固定。选择版本不等于已确认全部兼容。

在 Python 3.12、Node.js 22/npm 10.9.8 环境中准备：

```bash
cd agentEnvOnSubstrate/integration/sdk
npm ci --ignore-scripts
python3.12 -m venv .venv
.venv/bin/python -m pip install --require-hashes -r requirements.lock
```

两个后端需要同名 `AENV_TEMPLATE_ID` 测试模板和相同的 `E2B_COMPAT_USER_IMAGE` 镜像摘要。使用隔离的验收租户；原版脚本会创建、构建、暂停、恢复和删除沙箱/模板/卷。

必需环境变量：

- `AENV_STANDALONE_API_URL`、`AENV_STANDALONE_SANDBOX_URL`、`AENV_STANDALONE_API_KEY`
- `AENV_SUBSTRATE_API_URL`、`AENV_SUBSTRATE_SANDBOX_URL`、`AENV_SUBSTRATE_API_KEY`
- `AENV_TEMPLATE_ID`
- `E2B_COMPAT_USER_IMAGE`，必须是 `registry/image@sha256:<64 位摘要>`

执行：

```bash
.venv/bin/python run-comparison.py --report-dir ../reports/sdk-run-001
```

报告目录必须尚不存在。缺少目标、工具、依赖，版本不符，原版测试跳过，命令失败或超时都会失败。报告包含脚本摘要、版本、每个后端的测试结果与脱敏日志；结果只代表这些脚本的断言，不能代替完整响应/错误语义对照。超时强制结束测试进程后，须按日志核对服务端测试资源是否清理完成。

当前验证状态：锁文件已生成，入口前置条件测试已验证；未提供两个运行中的后端，SDK 服务端对照尚未执行。`aenv` CLI 固定源码基线，但 CLI 功能对照和 code-interpreter 用例尚未纳入此入口。
