# CLIProxyAPI 的 Codex PAT 插件

[English](README.md)

`codex-pat` 是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
（CPA）的原生插件，用于导入和管理 Codex Personal Access Token（PAT）。它负责
校验 PAT、保存兼容 CPA 的原生 Codex 凭据；模型发现、调度、协议转换和请求执行
仍由 CPA 完成。

本插件不是 OpenAI API Key Provider，也不自行实现模型执行器。

## 兼容性

- 插件版本：`0.1.4`
- 已验证 CPA 版本：`v7.2.102`（原生插件 ABI/schema v1）
- 发布目标：Linux amd64/arm64、macOS arm64（仅 Apple Silicon）和 Windows
  amd64。只有对应原生 runner 完成构建，且 CPA v7.2.102 成功加载、注册并完成
  一次 host callback 后，才会发布该平台 zip。
- 已发布 Linux 产物采用 manylinux2014 基线；如依赖高于 GLIBC 2.17 的符号，
  发布校验会拒绝该产物。

必须选择与 CPA 操作系统和架构一致的发布库。即使其他 CPA 版本同为 ABI v1，
也不代表已经兼容。
macOS Intel 不受支持，因为 Go 不支持 Darwin amd64 下这种进程内 Go host 与
Go `c-shared` 多 runtime 组合。

## 安装

`codex-pat` 正式上架后，优先通过 CPA Plugins Store 安装。手动安装时，从最新
GitHub Release 下载对应平台 zip，校验 `checksums.txt`，再把压缩包中唯一的
动态库解压到 CPA 插件目录。压缩包根目录中的库名如下：

| 平台 | 动态库 |
|---|---|
| Linux | `codex-pat.so` |
| macOS | `codex-pat.dylib` |
| Windows | `codex-pat.dll` |

替换已加载的动态库前先停止 CPA，并启用插件：

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
        version: "0.1.4"
```

启动 CPA，在 `GET /v0/management/plugins` 中确认 `codex-pat` 已注册且实际启用，
然后打开：

```text
http://127.0.0.1:8317/v0/resource/plugins/codex-pat/manage
```

在页面中输入 CPA 管理密钥和 PAT。CPA 注册凭据及模型期间，初始状态可能短暂
显示为 `pending`。

## 安全说明

- CPA 原生插件是进程内受信任代码，不是沙箱扩展。
- PAT 会以 `access_token` 保存到 CPA 的 auth 目录；必须保护 CPA 主机、配置、
  auth 目录、备份、日志和崩溃转储。
- 除非另有独立网络安全层保护，否则管理接口应仅绑定本机。
- 不要把 PAT 放入 URL、命令行参数、Issue、聊天内容或未脱敏日志。
- 设置 `CODEX_AUTHAPI_BASE_URL` 后，插件会向该地址发送 Bearer PAT；只能使用
  运维者控制的 HTTP(S) 地址。
- 禁用插件不会删除已导入的 auth 文件。若凭据必须停止使用，应在移除插件前
  通过 CPA 管理接口删除。

CPA v7.2.102 无法把语法错误且未注册的 JSON 文件暴露给插件做冲突检查。不要
手动在 `codex-pat-` 命名空间创建文件；如有无效冲突文件，请先移走再导入。

## 升级与移除

升级前，把回滚副本保存在所有 CPA 插件发现目录之外；停止 CPA，安装新版本化
产物，更新 `store.version`，重启后验证注册状态和一次模型请求。直接覆盖已经
加载的同一路径不会重新加载原生代码。CPA 启动后可能清理未选中的旧版本文件，
因此回滚副本不能留在发现目录中。

移除时，应先在插件仍启用的情况下通过 CPA 管理接口删除插件凭据，再禁用插件、
重启 CPA 并删除动态库。

## 构建与测试

需要 Go 1.26、CGO 和本机 C 工具链。

```bash
make check
make integration CPA_SOURCE=../CLIProxyAPI
```

`make check` 执行格式化、单元测试、vet、模块校验、本机动态库构建和 ABI 检查。
`make integration` 在 Linux/amd64 上针对干净的 CPA `v7.2.102` 源码运行完整的
伪 PAT 生命周期测试；它不会连接 OpenAI，也不需要真实凭据。

发布维护者可用 `make package` 打包当前本机目标；从四个原生 runner 收齐产物后，
用 `make verify-release` 验证完整发布集合。

## 支持

请通过 GitHub Issues 提交可复现问题，但不要附带凭据、auth 文件内容、管理密钥、
完整账号 ID 或未脱敏日志。请提供 CPA 版本、插件版本、操作系统、架构和脱敏后的
错误信息。

本项目采用 [MIT License](LICENSE)。
