#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOTMPDIR="${GOTMPDIR:-/tmp}"

echo "==> [lint] Go formatting (gofmt)"
unformatted="$(gofmt -l server)"
if [ -n "$unformatted" ]; then
  echo "ERROR: unformatted Go files (run 'gofmt -w server'):" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> [lint] Go static analysis (go vet)"
go -C server vet ./...

echo "==> [lint] Shell scripts syntax (bash -n)"
sh_files=(macos/Scripts/*.sh scripts/*.sh)
for f in "${sh_files[@]}"; do
  [ -f "$f" ] || continue
  bash -n "$f"
done
if command -v shellcheck >/dev/null 2>&1; then
  echo "==> [lint] shellcheck"
  shellcheck -S warning "${sh_files[@]}"
fi

if command -v node >/dev/null 2>&1; then
  echo "==> [lint] Web UI JavaScript syntax (node --check)"
  node --check server/internal/web/static/app.js
else
  echo "WARN: node not installed; skipping app.js syntax check" >&2
fi

echo "==> [lint] Secret & hygiene guard"
python3 - <<'PY'
import pathlib, re, subprocess, sys

patterns = [
    ("private key block", re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----")),
    ("Google AIza live key", re.compile(r"\bAIzaSy[0-9A-Za-z_-]{33}\b")),
    ("OpenAI live key", re.compile(r"\bsk-proj-[0-9A-Za-z_-]{20,}\b")),
]

tracked = subprocess.check_output(["git", "ls-files"], text=True).splitlines()
hits = []
for rel in tracked:
    if rel.endswith((".svg", ".png", ".icns", ".zip", ".dmg")):
        continue
    p = pathlib.Path(rel)
    if not p.is_file():
        continue
    text = p.read_text(encoding="utf-8", errors="ignore")
    for label, pat in patterns:
        if pat.search(text):
            hits.append(f"{rel}: matched {label}")

if hits:
    print("ERROR: potential hardcoded secrets detected:", file=sys.stderr)
    for h in hits:
        print("  -", h, file=sys.stderr)
    sys.exit(1)
PY

if [ "$(uname -s)" = "Darwin" ] && [ "${SUBBAR_SKIP_SWIFT_LINT:-0}" != "1" ]; then
  echo "==> [lint] macOS Swift compile check"
  CLANG_MODULE_CACHE_PATH="${CLANG_MODULE_CACHE_PATH:-/tmp/clang-module-cache}" \
  SWIFTPM_MODULECACHE_OVERRIDE="${SWIFTPM_MODULECACHE_OVERRIDE:-/tmp/swiftpm-module-cache}" \
    swift build --disable-sandbox --package-path macos
fi

echo "==> [lint] All lint checks passed."
