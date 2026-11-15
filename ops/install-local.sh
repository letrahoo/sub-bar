#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$HOME/.local/share/sub-cost-view" "$HOME/Library/Application Support/SubCostView"
chmod 700 "$HOME/Library/Application Support/SubCostView"

cp -f "$ROOT_DIR/dist/subbar-server" "$HOME/.local/share/sub-cost-view/subbar-server"
chmod 755 "$HOME/.local/share/sub-cost-view/subbar-server"

if [ -w "/Applications" ]; then
  rm -rf "/Applications/SubBar.app"
  cp -R "$ROOT_DIR/dist/SubBar.app" "/Applications/SubBar.app"
  echo "Installed /Applications/SubBar.app"
else
  mkdir -p "$HOME/Applications"
  rm -rf "$HOME/Applications/SubBar.app"
  cp -R "$ROOT_DIR/dist/SubBar.app" "$HOME/Applications/SubBar.app"
  echo "Installed $HOME/Applications/SubBar.app"
fi
