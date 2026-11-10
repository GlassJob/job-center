#!/usr/bin/env python3
"""WS Gateway 1.1 smoke: auth → register → Admin trigger → task.execute 回包 → close.

用法（需服务已启动）:
  set JC_CLIENT_ID=<bootstrap client id>
  py -3 scripts/ws_smoke.py

依赖: pip install websocket-client
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import secrets
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

try:
    import websocket
except ImportError:
    print("需要: pip install websocket-client", file=sys.stderr)
    sys.exit(2)

BASE = os.environ.get("JC_BASE_URL", "http://127.0.0.1:8080").rstrip("/")
WS_URL = os.environ.get("JC_WS_URL", BASE.replace("http://", "ws://").replace("https://", "wss://") + "/v1/executor/ws")
CLIENT_ID = os.environ.get("JC_CLIENT_ID", "")
CLIENT_SECRET = os.environ.get("JC_CLIENT_SECRET", "dev-admin-secret-please-rotate-43charsXX")
OPERATOR_ID = os.environ.get("JC_OPERATOR_ID", "smoke")
OPERATOR_NAME = os.environ.get("JC_OPERATOR_NAME", "ws-smoke")


def sha256_hex(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


def admin_sign(method: str, path: str, body: bytes) -> dict[str, str]:
    if not CLIENT_ID:
        raise SystemExit("请设置 JC_CLIENT_ID（bootstrap client id）")
    ts = str(int(time.time()))
    nonce = secrets.token_hex(8)
    canon = "\n".join(
        [
            method.upper(),
            path,
            "",
            CLIENT_ID,
            ts,
            nonce,
            OPERATOR_ID,
            OPERATOR_NAME,
            sha256_hex(body),
        ]
    )
    sig = hmac.new(CLIENT_SECRET.encode(), canon.encode(), hashlib.sha256).hexdigest()
    return {
        "Content-Type": "application/json",
        "X-JC-Client-Id": CLIENT_ID,
        "X-JC-Timestamp": ts,
        "X-JC-Nonce": nonce,
        "X-JC-Operator-Id": OPERATOR_ID,
        "X-JC-Operator-Name": OPERATOR_NAME,
        "X-JC-Signature": sig,
    }


def admin_json(method: str, path: str, payload: dict | None = None) -> dict:
    body = b"" if payload is None else json.dumps(payload, separators=(",", ":")).encode()
    req = urllib.request.Request(
        BASE + path,
        data=body if method.upper() != "GET" else None,
        headers=admin_sign(method, path, body),
        method=method.upper(),
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        err = e.read().decode(errors="replace")
        raise SystemExit(f"admin {method} {path} -> {e.code}: {err}") from e


def ensure_credential() -> tuple[str, str, str, str]:
    """returns code, credential_id, secret, service_id"""
    code = os.environ.get("JC_WS_CODE", "").strip()
    cred_id = os.environ.get("JC_WS_CREDENTIAL_ID", "").strip()
    secret = os.environ.get("JC_WS_SECRET", "").strip()
    service_id = os.environ.get("JC_WS_SERVICE_ID", "").strip()
    if code and cred_id and secret and service_id:
        return code, cred_id, secret, service_id

    code = code or f"ws.smoke.{secrets.token_hex(3)}"
    created = admin_json(
        "POST",
        "/v1/admin/services",
        {"code": code, "name": "WS Smoke", "description": "ws_smoke.py"},
    )
    data = created.get("data") or created
    sid = data["id"]
    cred = admin_json(
        "POST",
        f"/v1/admin/services/{sid}/credentials",
        {"name": "smoke-cred"},
    )
    cdata = cred.get("data") or cred
    return code, cdata["id"], cdata["secret"], sid


def envelope(typ: str, data: dict, request_id: str | None = None) -> dict:
    env = {
        "protocol_version": "1.1",
        "message_id": "m_" + secrets.token_hex(6),
        "type": typ,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime()),
        "data": data,
    }
    if request_id:
        env["request_id"] = request_id
    return env


def executor_sig(secret: str, code: str, cred_id: str, instance_key: str, ts: str, nonce: str) -> str:
    canon = "\n".join(["AUTH", code, cred_id, instance_key, ts, nonce])
    return hmac.new(secret.encode(), canon.encode(), hashlib.sha256).hexdigest()


def recv_typed(ws: websocket.WebSocket, expect: str, timeout: float = 10.0) -> dict:
    ws.settimeout(timeout)
    raw = ws.recv()
    env = json.loads(raw)
    if env.get("type") != expect:
        raise SystemExit(f"期望 {expect}，收到 {env.get('type')}: {raw}")
    return env


def handle_task_execute(ws: websocket.WebSocket, env: dict) -> None:
    data = env.get("data") or {}
    attempt_id = data["attempt_id"]
    lease = data["lease_token"]
    print(f"got task.execute attempt={attempt_id} job={data.get('job_code')}")
    ws.send(
        json.dumps(
            envelope(
                "task.execute_ack",
                {"attempt_id": attempt_id, "lease_token": lease, "accepted": True},
                env.get("message_id"),
            )
        )
    )
    _ = recv_typed(ws, "task.result_ack", timeout=5)
    started_at = time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime())
    ws.send(
        json.dumps(
            envelope(
                "task.started",
                {"attempt_id": attempt_id, "lease_token": lease, "started_at": started_at},
            )
        )
    )
    _ = recv_typed(ws, "task.result_ack", timeout=5)
    finished = time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime())
    ws.send(
        json.dumps(
            envelope(
                "task.completed",
                {
                    "attempt_id": attempt_id,
                    "lease_token": lease,
                    "finished_at": finished,
                    "result": {"ok": True, "from": "ws_smoke"},
                },
            )
        )
    )
    ack = recv_typed(ws, "task.result_ack", timeout=5)
    print(f"task completed ack={ack.get('data')}")


def main() -> None:
    code, cred_id, secret, service_id = ensure_credential()
    instance_key = os.environ.get("JC_WS_INSTANCE_KEY", f"smoke-{uuid.uuid4().hex[:8]}")
    print(f"code={code} credential_id={cred_id} service_id={service_id} instance_key={instance_key}")

    ws = websocket.create_connection(
        WS_URL,
        subprotocols=["jobcenter.v1"],
        timeout=15,
    )
    job_id = ""
    try:
        ts = str(int(time.time()))
        nonce = secrets.token_hex(8)
        auth_req = envelope(
            "auth.request",
            {
                "method": "hmac",
                "code": code,
                "credential_id": cred_id,
                "instance_key": instance_key,
                "timestamp": ts,
                "nonce": nonce,
                "signature": executor_sig(secret, code, cred_id, instance_key, ts, nonce),
                "protocol_versions": ["1.1"],
                "sdk": {"language": "python", "version": "0.0.1"},
            },
        )
        ws.send(json.dumps(auth_req))
        auth_env = recv_typed(ws, "auth.response")
        auth = auth_env["data"]
        if not auth.get("ok"):
            raise SystemExit(f"auth failed: {auth}")
        print(f"auth ok session_epoch={auth.get('session_epoch')} instance_id={auth.get('instance_id')}")

        reg = envelope(
            "service.register",
            {
                "code": code,
                "instance_key": instance_key,
                "environment": "dev",
                "host": {"hostname": "smoke-host", "pid": os.getpid()},
                "runtime": {"language": "python", "version": sys.version.split()[0]},
                "deployment": {"version": "smoke"},
                "capacity": {"max_concurrency": 2},
                "sdk_version": "0.0.1",
                "jobs": [
                    {
                        "job_code": "smoke.hello",
                        "job_name": "Smoke Hello",
                        "handler_version": "1.0.0",
                        "default_cron": "0 */5 * * * *",
                        "timeout_seconds": 60,
                        "params_schema": {"type": "object", "properties": {}},
                    }
                ],
            },
        )
        ws.send(json.dumps(reg))
        reg_env = recv_typed(ws, "service.register_ack")
        reg_data = reg_env["data"]
        if not reg_data.get("ok"):
            raise SystemExit(f"register failed: {reg_data}")
        accepted = reg_data.get("accepted_jobs") or []
        if not accepted:
            raise SystemExit(f"no accepted jobs: {reg_data}")
        job_id = accepted[0]["job_id"]
        print(f"register ok job_id={job_id}")

        hb = envelope("service.heartbeat", {"status": "ONLINE", "running": 0})
        ws.send(json.dumps(hb))
        hb_env = recv_typed(ws, "service.heartbeat_ack")
        print(f"heartbeat ok status={hb_env['data'].get('instance_status')}")

        ready = threading.Event()
        err_box: list[BaseException] = []

        def listen() -> None:
            try:
                ready.set()
                ws.settimeout(20)
                while True:
                    raw = ws.recv()
                    env = json.loads(raw)
                    typ = env.get("type")
                    if typ == "task.execute":
                        handle_task_execute(ws, env)
                        return
                    if typ in ("system.error", "system.notice"):
                        print("notice/error", env)
                        continue
                    print("ignore frame", typ)
            except BaseException as e:
                err_box.append(e)

        t = threading.Thread(target=listen, daemon=True)
        t.start()
        if not ready.wait(2):
            raise SystemExit("listener not ready")
        time.sleep(0.3)

        trigger = admin_json(
            "POST",
            f"/v1/admin/services/{service_id}/jobs/{job_id}/executions",
            {"params": {"from": "smoke"}, "idempotency_key": f"smoke-{secrets.token_hex(4)}"},
        )
        tdata = trigger.get("data") or trigger
        print(f"trigger status={tdata.get('status')} execution_id={tdata.get('execution_id')}")
        if tdata.get("status") == "QUEUED":
            raise SystemExit("execution queued — instance not picked; check ONLINE registration")

        t.join(timeout=25)
        if err_box:
            raise SystemExit(f"listener failed: {err_box[0]}")
        if t.is_alive():
            raise SystemExit("timeout waiting for task.execute")

        detail = admin_json(
            "GET",
            f"/v1/admin/services/{service_id}/jobs/{job_id}/executions/{tdata['execution_id']}",
        )
        d = detail.get("data") or detail
        print(f"final execution status={d.get('status')} result={d.get('result')}")
        if d.get("status") != "SUCCEEDED":
            raise SystemExit(f"expected SUCCEEDED, got {d.get('status')}")

        un = envelope("service.unregister", {"reason": "smoke done"})
        ws.send(json.dumps(un))
        time.sleep(0.2)
    finally:
        ws.close()
    print("OK")


if __name__ == "__main__":
    main()
