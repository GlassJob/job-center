#!/usr/bin/env bash
# 从 job-docs 同步 WebSocket JSON Schema 到 internal/protocol/schemas（默认 ../docs）。
set -euo pipefail
cd "$(dirname "$0")/.."
DOCS=${JC_DOCS_DIR:-../docs}
cp "$DOCS"/schemas/*.json internal/protocol/schemas/
echo "synced $(ls internal/protocol/schemas/*.json | wc -l) schemas from $DOCS"
