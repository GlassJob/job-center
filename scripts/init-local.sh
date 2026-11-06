#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCAL_CFG="${ROOT_DIR}/configs/local.yaml"

if [[ -f "${LOCAL_CFG}" ]]; then
  echo "configs/local.yaml 已存在，跳过生成。"
  exit 0
fi

DSN="${JC_DB_DSN:-job_center:job_center@tcp(127.0.0.1:3306)/job_center}"
PEPPER="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
ADMIN_SECRET="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"

umask 077
cat > "${LOCAL_CFG}" <<YAML
node_id: ""
http:
  executor_addr: ":8080"
  admin_addr: "127.0.0.1:8081"
  docs_enabled: true
database:
  dsn: "${DSN}"
security:
  credential_pepper: "${PEPPER}"
  admin_clients:
    glassadmin:
      secret: "${ADMIN_SECRET}"
      max_perms: ["*"]
      allowed_tasks: ["*"]
log:
  level: info
  format: text
YAML

echo "已生成 ${LOCAL_CFG}"
echo "  Executor WS : ws://127.0.0.1:8080/v1/executor/ws"
echo "  Admin / Docs: http://127.0.0.1:8081/swagger/"
echo "  glassadmin HMAC Secret: ${ADMIN_SECRET}"
