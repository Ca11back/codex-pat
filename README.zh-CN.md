# codex-pat

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）的原生插件，
用于导入 Codex Personal Access Token（PAT）并将其作为 CPA 凭据管理。

[English](README.md)

## 职责边界

插件只负责凭据这一件事：向 Codex Auth API 校验 PAT，解析其对应的账号身份，
写入 CPA 可直接使用的原生 Codex 凭据文件。此后的模型发现、调度、协议转换、
请求执行仍全部属于 CPA，不受本插件影响。

由此带来几点需要事先说明的结论：

- 它不是 OpenAI API Key Provider。token 必须是以 `at-` 开头的 Codex PAT。
- 它不实现任何模型执行器。若 CPA 无法提供某个 Codex 模型，原因不在插件。
- 它只暴露 `auth_provider` 和 `management_api` 两个 SDK 接口，没有其它。

## 环境要求

- schema 1 或更高版本的 CPA host，参见[版本兼容性](#版本兼容性)。
- 与 CPA 的操作系统**和**架构均一致的发布库。

| 平台 | 发布 zip 中的动态库 |
|---|---|
| Linux amd64、arm64 | `codex-pat.so` |
| macOS arm64 | `codex-pat.dylib` |
| Windows amd64 | `codex-pat.dll` |

不支持 macOS Intel：Go 不支持在 Darwin amd64 上由进程内 Go host 加载 Go
`c-shared` 库。Linux 产物以 manylinux2014 为基线，发布校验会拒绝任何依赖高于
GLIBC 2.17 符号的产物。

## 安装

若 `codex-pat` 已在 CPA Plugins Store 上架，优先从商店安装并直接跳到第 3 步。

1. **下载并校验。** 从最新 GitHub Release 取得对应平台的 zip，用
   `checksums.txt` 校验。

2. **安装动态库。** 把 zip 中唯一的动态库解压到 CPA 的插件目录。如果该路径上
   已有被 CPA 加载的库，先停止 CPA。

3. **配置 CPA。**

   ```yaml
   remote-management:
     allow-remote: false
     secret-key: "请替换为高强度管理密钥"

   plugins:
     enabled: true
     configs:
       codex-pat:
         enabled: true
         store:
           version: "0.1.7"
   ```

4. **确认注册。** 启动 CPA，在 `GET /v0/management/plugins` 中确认 `codex-pat`
   已注册且实际启用。

5. **导入 token。** 打开管理页面，填入 CPA 管理密钥并粘贴 PAT：

   ```text
   http://127.0.0.1:8317/v0/resource/plugins/codex-pat/manage
   ```

## 凭据管理

管理页面列出本插件持有的全部凭据，并支持三种操作：导入新 PAT、读取当前状态、
以及对已有凭据重新向 Codex Auth API 校验。

插件支持多账号。每份凭据以其 token 推导出的账号身份为键，因此为已有凭据的账号
再次导入 PAT 会替换该凭据，而不会产生重复项。若导入的 token 身份与既有凭据
冲突且插件无法调和，该请求会以 `account_mismatch` 拒绝。

凭据展示的状态反映的是 CPA 对该 auth 文件的视角，而非 token 本身。导入后返回
`pending` 是设计如此——此时 CPA 尚未完成凭据与模型注册。若凭据长期停在
`pending` 而不转为 `ready`，应检查 host：文件写入完成时插件的职责就已结束。

### 校验需要联网

导入和重新校验各会向 Codex Auth API（`auth.openai.com/api/accounts`，可用
`CODEX_AUTHAPI_BASE_URL` 覆盖）发起一次 HTTPS 请求，用于确认 token 并取回要写
入凭据文件的 account ID、套餐类型和邮箱。因此这两个操作可能因与 token 无关的
原因失败——该端点可能限流或暂时不可用，此时错误会标记为可重试，重新导入即可。

这是插件产生的唯一出站流量。真实模型请求上的限流由 CPA 负责，不会到达插件。

错误响应形如 `{"error": {"code", "message", "retryable"}}`，且在构造上即已
脱敏：任何错误路径都不包含 token 或上游响应体。

### HTTP 接口

管理页面本身就是这几个接口的客户端。它们接受 CPA 管理密钥，也可以直接调用。

| 方法 | 路径 | 用途 |
|---|---|---|
| `POST` | `/v0/management/plugins/codex-pat/import` | 导入 PAT，请求体 `{"pat": "at-..."}` |
| `GET` | `/v0/management/plugins/codex-pat/status` | 列出插件持有的凭据及其状态 |
| `POST` | `/v0/management/plugins/codex-pat/revalidate` | 重新校验单份凭据，请求体 `{"auth_index": "..."}` |

## 运维与安全

**信任模型。** CPA 原生插件是进程内受信任代码，不是沙箱扩展。安装一个插件等于
把 CPA 拥有的一切权限交给它。

**token 的存放位置。** PAT 以 `access_token` 形式保存在 CPA auth 目录下以
`codex-pat-` 为前缀的文件中。任何能读取该目录的主体都能读到 token，这一范围
还包括主机的配置、备份、日志和崩溃转储。

**使用习惯。** 不要把 PAT 放进 URL、命令行参数、Issue、聊天记录或未脱敏日志。
除非另有独立网络安全层防护，管理接口应只绑定本机。

**`CODEX_AUTHAPI_BASE_URL`。** 设置后，校验请求会以 Bearer PAT 发往该地址。
只能指向你自己运维的端点。

**禁用不等于吊销。** 禁用插件不会删除已导入的 auth 文件，它们仍然可用。若某些
凭据必须停止工作，应在移除插件前通过 CPA 管理接口删除。

**命名空间冲突。** 对于 CPA auth manager 未注册的文件，auth callback 无法把
语法错误的 JSON 交给插件，因此插件无法检测这类冲突（已在 CPA v7.2.102 与
v7.2.103 上验证）。不要手动创建 `codex-pat-` 前缀的文件；已存在的无效文件请先
移走再导入。

## 版本兼容性

| 插件版本 | 已验证的 CPA 版本 | Host schema | 声明的 schema |
|---|---|---:|---:|
| `v0.1.7`（当前） | `v7.2.102`、`v7.2.103`、`v7.2.129`、`v7.2.144` | 1–4 | 1 |
| `v0.1.6` | `v7.2.103`、`v7.2.129` | 2、3 | 2 |
| `v0.1.5` | `v7.2.103` | 2 | 2 |
| `v0.1.4` | `v7.2.102` | 1 | 1 |

v0.1.7 基于 CPA v7.2.144 SDK 构建，该 SDK 最新 RPC schema 为 4，但插件有意
声明 schema 1。由于它只使用自 schema 1 起未变过的 `auth_provider` 和
`management_api`，因此接受任何 schema ≥ 1 的 host，并返回 schema-1 契约。比
v7.2.144 更新的 host 会基于同一组接口向前容忍，但未经验证，也不会因此获得新
能力。

schema 2 中可选的生命周期（请求完成回调、活动请求终止）既未实现也未声明。

只有在对应原生 runner 完成构建，且真实的 CPA v7.2.144 host 成功加载、注册并
完成一次 host callback 之后，才会发布该平台的 zip。历史 host 版本由兼容性审计
和单元契约测试覆盖，不在发布 smoke 范围内。与其它 CPA 构建同为 ABI v1 并不
意味着兼容。

## 升级、回滚与移除

以下流程由两条 host 行为决定：覆盖已加载路径上的字节不会重新加载原生代码；
CPA 启动时可能删除未被选中的版本化插件文件。

**升级**

1. 把当前产物复制到所有 CPA 插件发现目录之外。这就是回滚副本，必须放在发现
   目录之外才能在启动清理中留存。
2. 停止 CPA。
3. 安装新的版本化产物，更新 `store.version`。
4. 重启，确认注册状态，并发起一次模型请求。

从 v0.1.6 升级到 v0.1.7 不需要更换 CPA。在 CPA v7.2.102 上可以从 v0.1.4 直接
升级到 v0.1.7；schema-1 契约在所有已验证的更新 host 上同样可用。

**回滚到 v0.1.6** 需要 CPA v7.2.103 或更高版本：重新安装版本化的 v0.1.6 产物，
恢复版本固定值，重启。

**移除。** 先在插件仍启用的状态下通过 CPA 管理接口删除插件持有的凭据，再禁用
插件、重启 CPA、删除动态库。

## 开发

需要 Go 1.26、CGO 和本机 C 工具链。发布产物固定使用官方 CPA v7.2.144 二进制
记录的精确 Go 1.26.4 工具链。

```bash
make check
```

执行格式化、单元测试、release 测试、vet、模块校验、本机动态库构建和 ABI 检查。
注册测试覆盖 host schema 1 至 4 以及一个更大的假设值，同时保留对格式错误和
零值的拒绝行为。

```bash
make integration CPA_SOURCE=../CLIProxyAPI
```

在 Linux/amd64 上，针对干净的 CPA v7.2.144 源码检出，用伪 PAT 运行完整生命
周期。既不访问 OpenAI，也不使用任何真实账号。

发布维护者另外使用 `make package` 打包当前本机目标，并在收齐四个原生 runner
的产物后运行 `make verify-release`。

## 问题反馈

在 GitHub Issues 提交可复现问题，附上 CPA 版本、插件版本、操作系统、架构和
脱敏后的报错信息。不要附带凭据、auth 文件内容、管理密钥、完整账号 ID 或原始
日志。

采用 [MIT License](LICENSE)。
