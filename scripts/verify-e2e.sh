#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOTMPDIR="${GOTMPDIR:-/tmp}"

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/subbar-e2e.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

SERVER_BIN="${SUBBAR_SERVER_BIN:-}"
if [ -z "$SERVER_BIN" ]; then
  SERVER_BIN="$TMP_DIR/subbar-server"
  echo "==> [e2e] Building subbar-server binary for black-box acceptance..."
  BUILD_FLAGS=()
  if [ "$(uname -s)" = "Darwin" ]; then
    BUILD_FLAGS=(-ldflags="-linkmode=external")
  fi
  go -C server build "${BUILD_FLAGS[@]}" -o "$SERVER_BIN" ./cmd/subbar-server
fi

echo "==> [e2e] Running automated black-box acceptance suite..."
python3 - "$SERVER_BIN" "$TMP_DIR" <<'PY'
import http.server
import json
import os
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

server_bin = sys.argv[1]
work_dir = sys.argv[2]
data_dir = os.path.join(work_dir, "data")


def pick_free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_call(url, method="GET", payload=None, headers=None):
    hdrs = dict(headers or {})
    data = None
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=8) as resp:
            return resp.status, dict(resp.headers), resp.read().decode("utf-8")
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode("utf-8", errors="replace")


class MockUpstreamState:
    mode = "ok"  # ok | 429 | 402 | 503
    seen_header_key = False
    seen_query_key = False


class MockGeminiHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def _send_json(self, code, obj):
        raw = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _check_auth(self):
        if "key=" in self.path:
            MockUpstreamState.seen_query_key = True
        if self.headers.get("x-goog-api-key"):
            MockUpstreamState.seen_header_key = True

    def do_GET(self):
        self._check_auth()
        if MockUpstreamState.mode == "503":
            self._send_json(503, {"error": {"code": 503, "message": "upstream maintenance"}})
            return
        if self.path.startswith("/v1beta/models"):
            self._send_json(200, {
                "models": [
                    {"name": "models/gemini-2.5-pro", "supportedGenerationMethods": ["generateContent", "countTokens"]},
                    {"name": "models/gemini-2.5-flash", "supportedGenerationMethods": ["generateContent", "countTokens"]},
                ]
            })
            return
        self._send_json(404, {"error": {"code": 404, "message": "not found"}})

    def do_POST(self):
        self._check_auth()
        length = int(self.headers.get("Content-Length", "0"))
        if length > 0:
            self.rfile.read(length)
        if MockUpstreamState.mode == "503":
            self._send_json(503, {"error": {"code": 503, "message": "upstream maintenance"}})
            return
        if MockUpstreamState.mode == "429":
            self._send_json(429, {
                "error": {
                    "code": 429,
                    "message": "Quota exceeded, limit: 0",
                    "status": "RESOURCE_EXHAUSTED",
                    "details": [
                        {
                            "@type": "type.googleapis.com/google.rpc.QuotaFailure",
                            "violations": [{"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests"}],
                        },
                        {
                            "@type": "type.googleapis.com/google.rpc.RetryInfo",
                            "retryDelay": "42s",
                        },
                    ],
                }
            })
            return
        if MockUpstreamState.mode == "402":
            self._send_json(402, {"error": {"code": 402, "message": "Prepaid balance depleted"}})
            return
        self._send_json(200, {"candidates": [{"content": {"parts": [{"text": "pong"}]}}]})


mock_port = pick_free_port()
mock_server = http.server.ThreadingHTTPServer(("127.0.0.1", mock_port), MockGeminiHandler)
mock_thread = threading.Thread(target=mock_server.serve_forever, daemon=True)
mock_thread.start()


def start_server(port):
    proc = subprocess.Popen(
        [server_bin, "-addr", f"127.0.0.1:{port}", "-data-dir", data_dir],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    base = f"http://127.0.0.1:{port}"
    deadline = time.time() + 10.0
    while time.time() < deadline:
        if proc.poll() is not None:
            out = proc.stdout.read() if proc.stdout else ""
            raise RuntimeError(f"subbar-server exited early ({proc.returncode}): {out}")
        try:
            code, _, body = http_call(f"{base}/api/health")
            if code == 200 and json.loads(body).get("ok") is True:
                return proc, base
        except Exception:
            time.sleep(0.1)
    proc.terminate()
    out = ""
    try:
        out, _ = proc.communicate(timeout=2)
    except Exception:
        pass
    raise RuntimeError(f"timed out waiting for subbar-server /api/health (output: {out.strip()})")


def stop_server(proc):
    proc.send_signal(signal.SIGTERM)
    try:
        rc = proc.wait(timeout=8)
    except subprocess.TimeoutExpired:
        proc.kill()
        raise RuntimeError("subbar-server did not exit cleanly on SIGTERM")
    assert rc == 0, f"expected clean exit code 0 on SIGTERM, got {rc}"


try:
    # 1. Non-loopback bind must be rejected by default
    bad_port = pick_free_port()
    bad_proc = subprocess.run(
        [server_bin, "-addr", f"0.0.0.0:{bad_port}", "-data-dir", data_dir],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        timeout=5,
    )
    assert bad_proc.returncode != 0, "expected non-loopback bind without flag to fail"

    # 2. Boot real compiled binary and verify health, data dir perms, assets & security headers
    srv_port = pick_free_port()
    proc, base = start_server(srv_port)

    dir_mode = stat.S_IMODE(os.stat(data_dir).st_mode)
    assert dir_mode == 0o700, f"expected data dir mode 0700, got {oct(dir_mode)}"

    code, hdrs, body = http_call(f"{base}/api/health")
    hdrs_lower = {k.lower(): v for k, v in hdrs.items()}
    assert code == 200 and json.loads(body)["ok"] is True
    assert hdrs_lower.get("x-content-type-options") == "nosniff"
    assert hdrs_lower.get("referrer-policy") == "no-referrer"
    assert hdrs_lower.get("cache-control") == "no-store"

    for asset in ["/", "/app.js", "/app.css", "/material-icons.svg"]:
        acode, _, abody = http_call(f"{base}{asset}")
        assert acode == 200 and len(abody) > 0, f"embedded asset {asset} failed ({acode})"

    # 3. Verify CSRF protection
    c1, _, _ = http_call(f"{base}/api/snapshot", headers={"Sec-Fetch-Site": "cross-site"})
    assert c1 == 403, f"expected 403 for cross-site request, got {c1}"
    c2, _, _ = http_call(
        f"{base}/api/entries",
        method="POST",
        payload={"name": "Evil", "provider": "manual"},
        headers={"Origin": "https://evil.example.com"},
    )
    assert c2 == 403, f"expected 403 for foreign origin POST, got {c2}"

    # 4. Verify provider catalog includes gemini_api & antigravity_oauth
    pcode, _, pbody = http_call(f"{base}/api/providers")
    assert pcode == 200
    prov_ids = {p["id"] for p in json.loads(pbody)["providers"]}
    for required in ["antigravity_oauth", "codex_oauth", "claude_oauth", "kimi_oauth", "gemini_api", "deepseek", "openrouter", "manual"]:
        assert required in prov_ids, f"missing provider {required}"

    # 5. Create Gemini API Key entry against mock upstream (zero-cost models mode)
    secret_key = "AIzaTestAcceptanceKey9876"
    mock_base = f"http://127.0.0.1:{mock_port}"
    ccode, _, cbody = http_call(
        f"{base}/api/entries",
        method="POST",
        payload={
            "name": "Gemini Acceptance",
            "provider": "gemini_api",
            "enabled": True,
            "pinned": True,
            "baseUrl": mock_base,
            "probeMode": "models",
            "secret": secret_key,
        },
    )
    assert ccode == 201, f"create gemini_api failed ({ccode}): {cbody}"
    created = json.loads(cbody)
    entry_id = created["entry"]["id"]
    assert created["snapshot"]["status"] == "ok"
    assert created["snapshot"]["primaryMetric"] == "可用"
    assert created["snapshot"]["dashboardUrl"] == "https://aistudio.google.com/billing"
    assert len(created["snapshot"]["details"]) >= 3
    assert MockUpstreamState.seen_header_key and not MockUpstreamState.seen_query_key

    # Verify entries.json permissions (0600) and zero secret leakage in API responses
    entries_file = os.path.join(data_dir, "entries.json")
    file_mode = stat.S_IMODE(os.stat(entries_file).st_mode)
    assert file_mode == 0o600, f"expected entries.json mode 0600, got {oct(file_mode)}"

    ecode, _, ebody = http_call(f"{base}/api/entries")
    scode, _, sbody = http_call(f"{base}/api/snapshot")
    assert ecode == 200 and scode == 200
    assert secret_key not in ebody and secret_key not in sbody, "secret leaked in API response"
    snap = json.loads(sbody)
    assert snap["summary"]["availableBalances"] == [], f"unexpected fake balance: {snap['summary']}"

    # 6. Same-account + same-source dedup on write (same Gemini key with explicit /v1beta suffix)
    dcode, _, dbody = http_call(
        f"{base}/api/entries",
        method="POST",
        payload={
            "name": "Gemini Acceptance Active",
            "provider": "gemini_api",
            "enabled": True,
            "baseUrl": mock_base + "/v1beta/",
            "probeMode": "generate",
            "secret": secret_key,
        },
    )
    assert dcode == 201, f"dedup upsert failed ({dcode}): {dbody}"
    upserted = json.loads(dbody)
    assert upserted["entry"]["id"] == entry_id, "expected same Gemini key to merge into existing entry"
    assert upserted["entry"]["pinned"] is True, "expected pinned flag preserved on merge"

    # 7. Active probe status transitions: 429 warning -> 402 critical -> 503 stale
    MockUpstreamState.mode = "429"
    r429, _, _ = http_call(f"{base}/api/entries/{entry_id}/refresh", method="POST")
    assert r429 == 200
    snap429 = json.loads(http_call(f"{base}/api/snapshot")[2])
    assert snap429["entries"][0]["status"] == "warning"
    assert snap429["summary"]["warningCount"] == 1

    MockUpstreamState.mode = "402"
    r402, _, _ = http_call(f"{base}/api/entries/{entry_id}/refresh", method="POST")
    assert r402 == 200
    snap402 = json.loads(http_call(f"{base}/api/snapshot")[2])
    assert snap402["entries"][0]["status"] == "critical"
    assert snap402["summary"]["criticalCount"] == 1

    MockUpstreamState.mode = "503"
    r503, _, _ = http_call(f"{base}/api/entries/{entry_id}/refresh", method="POST")
    assert r503 == 200
    snap503 = json.loads(http_call(f"{base}/api/snapshot")[2])
    assert snap503["entries"][0]["status"] == "stale"
    assert len(snap503["entries"][0]["details"]) > 0

    # 8. Graceful shutdown, seed duplicate OAuth entries on disk, restart & verify startup dedup + persistence
    stop_server(proc)
    with open(entries_file, "r", encoding="utf-8") as fh:
        doc = json.load(fh)
    doc["entries"].extend([
        {
            "id": "agy-seed-1",
            "name": "Antigravity (dup)",
            "provider": "antigravity_oauth",
            "kind": "subscription",
            "enabled": True,
            "pinned": True,
            "sortOrder": 10,
            "accountEmail": "dup@example.com",
            "secret": "ya29.old",
            "createdAt": "2026-11-20T00:00:00Z",
            "updatedAt": "2026-11-20T00:00:00Z",
        },
        {
            "id": "agy-seed-2",
            "name": "Antigravity (dup)",
            "provider": "antigravity_oauth",
            "kind": "subscription",
            "enabled": True,
            "pinned": False,
            "sortOrder": 50,
            "accountEmail": "DUP@example.com",
            "secret": "ya29.new",
            "createdAt": "2026-11-24T00:00:00Z",
            "updatedAt": "2026-11-24T00:00:00Z",
        },
    ])
    with open(entries_file, "w", encoding="utf-8") as fh:
        json.dump(doc, fh)

    srv_port2 = pick_free_port()
    proc2, base2 = start_server(srv_port2)
    try:
        entries_after = json.loads(http_call(f"{base2}/api/entries")[2])["entries"]
        ids_after = [e["id"] for e in entries_after]
        assert len(entries_after) == 2, f"expected 2 entries (1 gemini + 1 deduped agy), got {ids_after}"
        assert entry_id in ids_after and "agy-seed-1" in ids_after and "agy-seed-2" not in ids_after

        # Clean delete
        for eid in ids_after:
            del_code, _, _ = http_call(f"{base2}/api/entries/{eid}", method="DELETE")
            assert del_code == 200
        assert len(json.loads(http_call(f"{base2}/api/entries")[2])["entries"]) == 0
    finally:
        stop_server(proc2)

    print("==> [e2e] All acceptance checks passed (12/12).")
finally:
    mock_server.shutdown()
PY
