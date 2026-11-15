# SubBar

常驻 macOS 菜单栏，一眼查看 AI 订阅配额和 API 余额。

[English](README.md)

- 菜单栏多种显示样式：双行、单行、进度条、状态点
- 5 小时 / 每周配额窗口与重置倒计时，API 余额
- 同一服务可添加多个账号，令牌自动续期
- 纯本地：内置 Go 服务负责轮询，密钥不出本机

## 安装

在 [Releases](../../releases) 下载 `SubBar-macOS.zip`（支持 Apple Silicon 和 Intel），解压后把 `SubBar.app` 拖入「应用程序」。

构建产物为 ad-hoc 签名，未公证。首次打开请右键选择「打开」，或执行：

```bash
xattr -dr com.apple.quarantine /Applications/SubBar.app
```

App 启动时会拉起内置服务（`127.0.0.1:8328`），退出时自动关闭。浏览器访问同一地址可看到相同面板。

## 支持的数据源

| 类型 | 模式 |
| --- | --- |
| Antigravity、Codex / ChatGPT、Claude、Kimi（OAuth） | 订阅配额 |
| DeepSeek、OpenRouter、Moonshot、SiliconFlow | API 余额 |
| OpenAI 兼容计费接口（NewAPI / OneAPI） | API 余额 |
| 自定义 HTTP JSON 接口 | 配额或余额 |
| 手动账本 / 固定周期订阅 | 配额或余额 |

## OAuth 客户端凭据

项目不内置任何 OAuth client_id / secret。使用某个服务的网页授权登录前，请在添加入口弹窗的 OAuth 区域填入该服务的客户端凭据。凭据保存在数据目录的 `oauth-clients.json`（权限 `0600`），接口不会回显。

也可用环境变量：`SUBBAR_OAUTH_<PROVIDER>_CLIENT_ID` 和 `_CLIENT_SECRET`，如 `SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID`。API Key 和手动类入口不需要。

## 数据与安全

- 数据目录：`~/Library/Application Support/SubCostView`（可用 `SUBBAR_DATA_DIR` 覆盖），权限 `0700`。
- 服务默认只绑定回环地址。如需对外访问，使用 `-allow-non-loopback` 并在前面加带认证的反向代理。
- 拒绝跨站浏览器请求。

## 构建

```bash
make test   # Go 测试
make all    # 生成 dist/subbar-server 与 dist/SubBar.app
```

需要 Go 1.22+ 与 Xcode 命令行工具。推送 `v*` 标签会通过 GitHub Actions 自动构建并发布。

## 许可证

MIT
