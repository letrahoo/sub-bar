# SubBar

A macOS menu bar app that shows your AI subscription quotas and API balances at a glance.

[中文说明](README.zh-CN.md)

- Menu bar status with compact, single-line, progress or dot styles
- Quota windows (5h / weekly) with reset countdowns, and API balances
- Multiple accounts per service, auto token refresh
- Local only: a bundled Go server polls providers; keys never leave your machine

## Install

Download `SubBar-macOS.dmg` or `SubBar-macOS.zip` from [Releases](../../releases) (Apple Silicon and Intel), and move `SubBar.app` to Applications. To build a DMG locally, run `make all` and open `dist/SubBar-macOS.dmg`.

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

Public OAuth client IDs for official CLI integrations (Antigravity, Codex, Claude, Kimi) are built in out of the box—browser login works with zero setup.

If needed, you can override any provider's client credentials in the OAuth section of the add-entry dialog (saved locally in `oauth-clients.json` with mode `0600`) or via environment variables `SUBBAR_OAUTH_<PROVIDER>_CLIENT_ID` and `_CLIENT_SECRET` (for example `SUBBAR_OAUTH_ANTIGRAVITY_OAUTH_CLIENT_ID`).

## Data and security

- Data directory: `~/Library/Application Support/SubBar` (override with `SUBBAR_DATA_DIR`), created `0700`.
- The server binds to loopback only. Use `-allow-non-loopback` to expose it, and put an authenticating proxy in front.
- Cross-site browser requests are rejected.

## Build

```bash
make test   # Go tests
make all    # dist/subbar-server and dist/SubBar.app
```

Requires Go 1.22+ and Xcode command line tools. Pushing a `v*` tag builds and publishes a release via GitHub Actions.

## License

SubBar is MIT licensed. [Google Material Icons](https://github.com/google/material-design-icons) are bundled locally under [Apache 2.0](server/internal/web/static/material-icons.LICENSE.txt).
