#!/usr/bin/env bash
set -euo pipefail
# M1 demo: login -> create model/template -> create deployment -> start/stop.
BASE="http://localhost:8080"
TOKEN=$(curl -s -X POST "$BASE/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin1234"}' | python -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
AUTH="Authorization: Bearer $TOKEN"

curl -s -X POST "$BASE/api/v1/models" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"qwen3-9b","task":"text-generation","framework":"llama"}' | python -m json.tool
curl -s -X POST "$BASE/api/v1/templates" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"llama-openai","runtime":"llama"}' | python -m json.tool
echo "--- deployments ---"
curl -s "$BASE/api/v1/deployments" -H "$AUTH" | python -m json.tool
echo "--- health ---"
curl -s "$BASE/health"
