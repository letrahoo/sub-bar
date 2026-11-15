# SubBar (`sub-cost-view`)

参考 [CodexBar](https://github.com/steipete/CodexBar) 交互体验从零构建的**极简高对比度订阅配额 & API 余额监控套件**：

1. **`subbar-server`（Go 标准库零外部依赖，~5.6 MB）**：常驻在 Mac Mini 回环地址 `127.0.0.1:8328`，统一负责定时轮询、单飞防重（Singleflight 防触发上游 429 限流）、瞬时故障缓存保活、密钥脱敏存储，并内嵌高对比度响应式 Web 面板。
2. **`SubBar.app`（Swift 原生 AppKit `NSStatusItem` + SwiftUI Popover，零第三方依赖，无 Dock 图标）**：常驻 macOS 顶部菜单栏，支持仿 CodexBar **双行紧凑置顶指标**、单行指标、迷你进度胶囊和极简状态点模式，点开即可查看/新增/管理所有订阅与余额。
3. **Levi Remote 手机端接入**：通过 Tailscale Serve HTTPS + Caddy 精确身份网关挂入 Levi Remote「我的服务」抽屉，无需弹密码框即可在手机上随时查看各家剩余配额与余额，也可直接在手机端新增或调整入口。

## 安装（无需源码，独立运行）

从 [Releases](../../releases) 下载 `SubBar-macOS.zip`（Apple Silicon / Intel 通用），解压后拖入「应用程序」。App 内置 `subbar-server`，启动时自动在 `127.0.0.1:8328` 拉起，退出时自动关闭；不依赖 Tailscale、Caddy 或其它服务。

> 构建产物仅做 ad-hoc 签名、未经 Apple 公证。首次打开请右键 App → 打开；若提示已损坏，执行：
> `xattr -dr com.apple.quarantine /Applications/SubBar.app`

密钥与配置保存在 `~/Library/Application Support/SubCostView/`，每台电脑独立，需各自添加账号。

发布新版本：`git tag v0.1.0 && git push --tags`，GitHub Actions 会自动构建并发布。

## OAuth 客户端凭据（需自行填写）

本项目**不内置任何 OAuth client_id / client_secret**。使用 Antigravity、Codex、Claude、Kimi 的「链接授权登录」前，在添加入口弹窗的「OAuth 客户端凭据」折叠区填入该服务官方 CLI 所用的凭据（Antigravity 另需 client_secret）。凭据保存在本机数据目录的 `oauth-clients.json`（权限 `0600`，不进 Git，接口不回显明文）。

也可用环境变量提供：`SUBBAR_OAUTH_<PROVIDER>_CLIENT_ID` / `_CLIENT_SECRET`，例如 `SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID`。API Key 类、手动账本等入口不需要这些凭据。

## 支持独立添加的数据源（不依赖任何代理网关）

每个入口完全独立配置，支持同一服务添加多个账号：

| 类型 ID | 名称 | 模式 | 说明 |
| --- | --- | --- | --- |
| `antigravity_oauth` | Antigravity (`agy`) | 订阅配额 | 点击授权链接跳转 Google 登录 → 本地临时回调自动捕获并轮询完成（手机端亦支持粘贴回调 URL），完整监控 Gemini 与 Claude / GPT 的 5h 限额及 7d 周限额重置倒计时，自动无感续期 |
| `codex_oauth` | OpenAI Codex / ChatGPT | 订阅配额 | 点击授权链接一键接入（支持多账号与工作区自动识别），监控 5h 会话窗口、7d 周窗口重置倒计时与消耗节奏，自动续期 |
| `claude_oauth` | Claude Pro / Max | 订阅配额 | 点击授权链接一键接入，监控 5h、7d、Sonnet/Opus 窗口及 Extra Usage 消费，自动续期 |
| `kimi_oauth` | Kimi 会员 / Kimi Code | 订阅配额 | 点击授权链接跳转 Kimi 官方登录页（原生支持**微信扫码登录**、手机号验证码或已登录会话一键确认，无需本地回调端口），监控 5h 速率窗口与周/月会员配额及重置倒计时，支持自动无感续期；亦兼容手动填入 `sk-kimi-...` 或 `kimi-auth` 凭证 |
| `deepseek` | DeepSeek API | API 余额 | 实时查询可用总额、充值余额与赠金（支持 CNY / USD） |
| `xai` | Grok (xAI) API 密钥 | API 密钥 | 校验密钥有效性 / 封禁状态，显示密钥名称（xAI 暂不开放普通 Key 余额查询） |
| `openrouter` | OpenRouter Credits | API 余额 | 自动兼容 Management Key（查总 Credits）与普通 API Key（查 Key 剩余限额与累计消耗） |
| `moonshot` | Moonshot 开放平台余额 | API 余额 | 查询月之暗面开放平台按量可用余额、现金与代金券余额 |
| `siliconflow` | SiliconFlow 硅基流动 | API 余额 | 查询总余额、充值余额与赠送余额 |
| `openai_billing` | 兼容中转站 / NewAPI / OneAPI | API 余额 | 支持任意实现 `/v1/dashboard/billing/subscription` + `usage` 的中转站 |
| `custom_http` | 自定义 HTTP JSON 接口 | 订阅 / 余额可切 | 零代码扩展任意私有或冷门服务：配置 URL、认证头、JSON 点号路径（如 `data.balance` 或 `data.used_percent`）与倍率 |
| `manual` | 独立周期订阅 / 手动账本 | 订阅 / 余额可切 | 适用于 Cursor Pro、Copilot、Perplexity、会员包月/包年或手动储值卡：自动计算下次重置倒计时、额度百分比与固定月费折算 |

## 构建与测试

```bash
make test    # 运行 Go 单元测试与安全边界检查
make all     # 编译 dist/subbar-server 与 dist/SubBar.app
```

## 本机部署与安全边界

- **只绑回环**：`subbar-server` 默认强制只监听 `127.0.0.1:8328`（非回环地址需显式传 `-allow-non-loopback`）。
- **私有存储**：运行配置与密钥原子写入 `~/Library/Application Support/SubCostView/`（目录 `0700`、文件 `0600`），不进 Git；所有读取接口仅返回脱敏掩码（如 `sk-••••9a2f`）。
- **跨站防护**：内置 `Sec-Fetch-Site: cross-site` 与 `Origin` 校验，阻断本地浏览器跨站请求伪造。
- **网关模板**：`launchd` 与 Caddy + Levi Remote 配置模板见 `ops/`。
