#!/usr/bin/env bash
# POST /v1/deployments — HMAC 客户端签名（无 X-JC-Operator）。例：
#   JC_URL=http://127.0.0.1:18081 JC_CLIENT=deployer JC_SECRET=dev-deployer-secret16 \
#   ./scripts/register-deployment.sh mysvc 1.2.3 abcdef1 build-42
set -euo pipefail
SERVICE=${1:?service_code}; VERSION=${2:?deployment_version}; COMMIT=${3:?git_commit}; BUILD=${4:?build_id}
ENV_NAME=${5:-dev}; STATUS=${6:-SUCCEEDED}
URL=${JC_URL:-http://127.0.0.1:18081}
CLIENT=${JC_CLIENT:-deployer}
SECRET=${JC_SECRET:?JC_SECRET required}
BODY=$(printf '{"service_code":"%s","environment":"%s","deployment_version":"%s","git_commit":"%s","build_id":"%s","deployment_status":"%s","set_expected":true}' \
  "$SERVICE" "$ENV_NAME" "$VERSION" "$COMMIT" "$BUILD" "$STATUS")
TS=$(date +%s); NONCE=$(openssl rand -hex 16)
BODY_HASH=$(printf '%s' "$BODY" | openssl dgst -sha256 | awk '{print $2}')
OP_HASH=$(printf '' | openssl dgst -sha256 | awk '{print $2}')
CANON=$(printf 'POST\n/v1/deployments\n\n%s\n%s\n%s\n%s' "$BODY_HASH" "$TS" "$NONCE" "$OP_HASH")
SIG=$(printf '%s' "$CANON" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}')
curl -sS -X POST "$URL/v1/deployments" \
  -H "Content-Type: application/json" \
  -H "X-JC-Client: $CLIENT" \
  -H "X-JC-Timestamp: $TS" \
  -H "X-JC-Nonce: $NONCE" \
  -H "X-JC-Signature: $SIG" \
  -d "$BODY"
echo
