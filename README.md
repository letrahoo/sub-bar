# SubBar

A macOS menu bar app that shows your AI subscription quotas and API balances at a glance.

[中文说明](README.zh-CN.md)

- Menu bar status with compact, single-line, progress or dot styles
- Quota windows (5h / weekly) with reset countdowns, and API balances
- Multiple accounts per service, auto token refresh
- Local only: a bundled Go server polls providers; keys never leave your machine

## Install

Download `SubBar-macOS.zip` from [Releases](../../releases) (Apple Silicon and Intel), unzip, and move `SubBar.app` to Applications.

The build is ad-hoc signed and not notarized. Right-click → Open on first launch, or:

```bash
xattr -dr com.apple.quarantine /Applications/SubBar.app
```

The app starts its bundled server on `127.0.0.1:8328` and stops it on quit. The same dashboard is available in a browser at that address.

## Supported sources

| Type | Mode |
| --- | --- |
| Antigravity, Codex / ChatGPT, Claude, Kimi (OAuth) | Subscription quota |
| DeepSeek, OpenRouter, Moonshot, SiliconFlow | API balance |
| OpenAI-compatible billing (NewAPI / OneAPI) | API balance |
| Custom HTTP JSON endpoint | Quota or balance |
| Manual ledger / fixed-period subscription | Quota or balance |

## OAuth client credentials

No OAuth client IDs or secrets are bundled. Before using browser login for a service, enter that service's client credentials in the OAuth section of the add-entry dialog. They are stored in `oauth-clients.json` in the data directory (mode `0600`) and are never returned by the API.

Environment variables also work: `SUBBAR_OAUTH_<PROVIDER>_CLIENT_ID` and `_CLIENT_SECRET`, for example `SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID`. API-key and manual sources need none of this.

## Data and security

- Data directory: `~/Library/Application Support/SubCostView` (override with `SUBBAR_DATA_DIR`), created `0700`.
- The server binds to loopback only. Use `-allow-non-loopback` to expose it, and put an authenticating proxy in front.
- Cross-site browser requests are rejected.

## Build

```bash
make test   # Go tests
make all    # dist/subbar-server and dist/SubBar.app
```

Requires Go 1.22+ and Xcode command line tools. Pushing a `v*` tag builds and publishes a release via GitHub Actions.

## License

MIT
