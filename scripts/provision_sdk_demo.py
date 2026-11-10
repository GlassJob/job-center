#!/usr/bin/env python3
"""创建/复用 sdk.demo 服务与凭据，打印 Go Executor 环境变量（PowerShell）。"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("JC_BASE_URL", "http://127.0.0.1:8080").rstrip("/")
CLIENT_ID = os.environ.get("JC_CLIENT_ID", "01a1c3ab-71fe-73bb-a56e-d2f7c419d3a7")
CLIENT_SECRET = os.environ.get("JC_CLIENT_SECRET", "dev-admin-secret-please-rotate-43charsXX")
CODE = os.environ.get("JC_SERVICE_CODE", "sdk.demo")


def sha256_hex(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


def admin_sign(method: str, path: str, query: str, body: bytes) -> dict[str, str]:
    ts = str(int(time.time()))
    nonce = secrets.token_hex(8)
    # QUERY 与服务端 CanonicalQuery 对齐：单键无特殊字符时直接用原串
    canon = "\n".join(
        [
            method.upper(),
            path,
            query,
            CLIENT_ID,
            ts,
            nonce,
            "sdk-provision",
            "sdk-provision",
            sha256_hex(body),
        ]
    )
    sig = hmac.new(CLIENT_SECRET.encode(), canon.encode(), hashlib.sha256).hexdigest()
    return {
        "Content-Type": "application/json",
        "X-JC-Client-Id": CLIENT_ID,
        "X-JC-Timestamp": ts,
        "X-JC-Nonce": nonce,
        "X-JC-Operator-Id": "sdk-provision",
        "X-JC-Operator-Name": "sdk-provision",
        "X-JC-Signature": sig,
    }


def admin_json(method: str, path: str, payload: dict | None = None, query: str = "") -> dict:
    body = b"" if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    url = BASE + path
    if query:
        url += "?" + query
    req = urllib.request.Request(
        url,
        data=body if method.upper() != "GET" else None,
        headers=admin_sign(method, path, query, body),
        method=method.upper(),
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        err = e.read().decode(errors="replace")
        raise SystemExit(f"admin {method} {path} -> {e.code}: {err}") from e


def main() -> None:
    # list services and find code
    listed = admin_json("GET", "/v1/admin/services", query="page_size=100")
    data = listed.get("data") or listed
    items = data.get("items") if isinstance(data, dict) else data
    if not isinstance(items, list):
        items = []
    sid = None
    for it in items:
        if it.get("code") == CODE:
            sid = it["id"]
            break
    if not sid:
        created = admin_json(
            "POST",
            "/v1/admin/services",
            {"code": CODE, "name": "SDK Demo", "description": "go-executor example"},
        )
        sid = (created.get("data") or created)["id"]
        print(f"# created service {CODE} id={sid}", file=sys.stderr)
    else:
        print(f"# reuse service {CODE} id={sid}", file=sys.stderr)

    cred = admin_json(
        "POST",
        f"/v1/admin/services/{sid}/credentials",
        {"name": f"go-executor-{secrets.token_hex(2)}", "allowed_ips": ["127.0.0.1", "::1"]},
    )
    cdata = cred.get("data") or cred
    cred_id = cdata["id"]
    secret = cdata["secret"]
    print(f"$env:JC_WS_ENDPOINT='ws://127.0.0.1:8080/v1/executor/ws'")
    print(f"$env:JC_SERVICE_CODE='{CODE}'")
    print(f"$env:JC_CREDENTIAL_ID='{cred_id}'")
    print(f"$env:JC_CREDENTIAL_SECRET='{secret}'")
    print(f"$env:JC_INSTANCE_KEY='go-executor-1'")
    print("# then: go run ./examples/go-executor")


if __name__ == "__main__":
    main()
