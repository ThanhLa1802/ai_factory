#!/usr/bin/env bash
# M2 demo: async deployment — POST /deployments -> 202 -> PENDING..READY.
# Requires: Go server + Postgres + Kafka running, jq installed.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
KAFKA_ADDR="${AI_FACTORY_KAFKA_ADDR:-localhost:9092}"
TOKEN=""

echo "==> 1. Kafka reachable?"
if ! (echo > "/dev/tcp/${KAFKA_ADDR/:/\/}") 2>/dev/null; then
  echo "Kafka not reachable at $KAFKA_ADDR. Start it:"
  echo "  docker compose -f deployments/docker-compose.yml up -d postgres kafka"
  exit 1
fi
echo "    OK ($KAFKA_ADDR)"

echo "==> 2. Server healthy?"
curl -fsS "$BASE/health" >/dev/null && echo "    OK ($BASE)"

echo "==> 3. Login (seeded admin/admin1234)"
LOGIN=$(curl -fsS -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin1234"}')
TOKEN=$(echo "$LOGIN" | jq -r .access_token)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || { echo "login failed"; exit 1; }

AUTH=(-H "Authorization: Bearer $TOKEN")

suffix=$(date +%s)

echo "==> 4. Create model + version, template + version"
MODEL=$(curl -fsS -X POST "$BASE/api/v1/models" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"qwen-m2-$suffix\",\"task\":\"text-generation\",\"framework\":\"transformers\"}")
MV=$(curl -fsS -X POST "$BASE/api/v1/models/$(echo "$MODEL" | jq -r .id)/versions" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d '{"version":"1.0","artifact_uri":"file:///models/qwen.gguf"}')
TPL=$(curl -fsS -X POST "$BASE/api/v1/templates" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"tpl-m2-$suffix\",\"runtime\":\"python\"}")
TV=$(curl -fsS -X POST "$BASE/api/v1/templates/$(echo "$TPL" | jq -r .id)/versions" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d '{"version":"1.0","image":"ai-factory:latest"}')
echo "    model_version=$(echo "$MV" | jq -r .id) template_version=$(echo "$TV" | jq -r .id)"

echo "==> 5. Create deployment -> 202 Accepted"
DEP=$(curl -fsS -X POST "$BASE/api/v1/deployments" "${AUTH[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"model_version_id\":\"$(echo "$MV" | jq -r .id)\",\"template_version_id\":\"$(echo "$TV" | jq -r .id)\",\"name\":\"svc-m2-$suffix\",\"region\":\"us-east-1\",\"desired_replicas\":1}")
DEP_ID=$(echo "$DEP" | jq -r .id)
echo "    deployment_id=$DEP_ID status=$(echo "$DEP" | jq -r .status)"

echo "==> 6. Poll until READY (max 15s)"
for i in $(seq 1 15); do
  STATUS=$(curl -fsS "$BASE/api/v1/deployments/$DEP_ID" "${AUTH[@]}" | jq -r .status)
  echo "    t=${i}s status=$STATUS"
  [ "$STATUS" = "READY" ] && break
  [ "$STATUS" = "FAILED" ] && { echo "    deployment FAILED"; exit 1; }
  sleep 1
done

echo "==> 7. Final deployment"
curl -fsS "$BASE/api/v1/deployments/$DEP_ID" "${AUTH[@]}" | jq '{id, status, workload_ref, region, desired_replicas}'
echo "DONE."
