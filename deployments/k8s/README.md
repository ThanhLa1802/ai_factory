# AI Factory trên Kubernetes (k3d + Kustomize)

Luyện deploy toàn bộ stack lên một cluster K8s local có GPU. Kiến trúc trên cluster:

```
Ingress (Traefik) ─► web:3000 ──proxy──► server:8080 ──HTTP (OpenAI)──► vllm:8000 (GPU)
                                              │
                        postgres:5432 · redis:6379 · kafka:29092
```

- **Go gọi thẳng vLLM** qua OpenAI-compatible API (`inference.mode=openai`) — **không có pod worker gRPC** trên đường request. Đây là pattern production (gateway → vLLM).
- `vllm` là pod riêng (Deployment + Service, `nvidia.com/gpu: 1`), model nhỏ `Qwen/Qwen2.5-1.5B-Instruct` để học.
- `server` chạy API + control-plane worker in-process, tự migrate + seed khi boot.
- `seedDemo` tạo model `qwen-3b` + deployment READY → chat chạy được **không cần Kafka**.
- Muốn chạy thêm đường worker/song song (engine `transformers` tự viết) → xem `optional/worker-grpc.yaml`.

## 0. Điều kiện

| Cần | Kiểm tra |
|---|---|
| Docker + GPU | `docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi` |
| nvidia-container-toolkit | `docker info \| grep -i runtime` → có `nvidia` |
| k3d ≥ 5.6, kubectl | `k3d version`, `kubectl version --client` |

## 1. Tạo cluster k3d có GPU

```bash
k3d cluster create ai-factory \
  --gpus all \
  -p "80:80@loadbalancer"        # để http://ai-factory.localhost vào Traefik
```

Không bind được port 80 thì bỏ `-p` và dùng `kubectl port-forward` ở bước 6.

## 2. Cài NVIDIA device plugin

```bash
kubectl apply -f deployments/k8s/gpu/nvidia-device-plugin.yaml

# Phải thấy nvidia.com/gpu: 1
kubectl get nodes -o jsonpath='{.items[*].status.allocatable}' | tr ' ' '\n' | grep nvidia
```

Chưa có `nvidia.com/gpu` thì pod `vllm` kẹt `Pending` — sửa GPU/plugin trước khi đi tiếp.

## 3. Build image

```bash
docker build -f go-server/Dockerfile -t ai-factory/server:dev .          # context = repo root
docker build -f web/Dockerfile \
  --build-arg AI_FACTORY_API_URL=http://server:8080 -t ai-factory/web:dev web
```

`vllm/vllm-openai` để cluster tự pull (cần internet).

## 4. Nạp image vào k3d

```bash
k3d image import ai-factory/server:dev ai-factory/web:dev -c ai-factory
```

## 5. Deploy

```bash
kubectl apply -k deployments/k8s/overlays/k3d
kubectl -n ai-factory get pods -w
```

Chờ `vllm` READY (lần đầu tải model ~vài phút; `startupProbe` đã chịu được), rồi `server`, `web`.

## 6. Truy cập

```bash
# Cách A — qua ingress (nếu map port 80)
open http://ai-factory.localhost

# Cách B — port-forward
kubectl -n ai-factory port-forward svc/web 3000:3000
open http://localhost:3000
```

Đăng nhập `admin / admin1234`, chat với model **`qwen-3b`** (routing key; backend thật là
`Qwen/Qwen2.5-1.5B-Instruct` do vLLM phục vụ).

Smoke test nhanh (bỏ qua UI):

```bash
kubectl -n ai-factory port-forward svc/server 8080:8080 &
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin1234"}' | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"model":"qwen-3b","messages":[{"role":"user","content":"Hello"}]}'
```

## 7. Dọn dẹp

```bash
k3d cluster delete ai-factory
```

## Ghi chú thiết kế

- **Hai đường inference** (`inference.mode`): `worker` (gRPC Python worker, default — dùng cho engine
  `transformers` tự viết) và `openai` (gọi thẳng upstream OpenAI-compatible — vLLM/llama-server).
  Base overlay dùng `openai`. Seam nằm ở `inference.Generator` (`BatchScheduler` | `OpenAIClient`).
- **1 GPU = 1 model.** `vllm` dùng `strategy: Recreate`. Không chạy song song vLLM + engine tự viết.
- **Đổi model:** sửa `--model` (vllm) và `AI_FACTORY_INFERENCE_MODEL` (server) cho khớp; HF cache ở PVC `vllm-cache` (`/root/.cache/huggingface`).
- **Tool calling** bật sẵn ở vLLM (`--enable-auto-tool-choice --tool-call-parser hermes`). Model 1.5B gọi tool yếu — chủ yếu để học luồng.
- **Đường worker (tùy chọn):** build `docker build -f python-worker/Dockerfile -t ai-factory/worker:dev python-worker`, import, `kubectl apply -f deployments/k8s/optional/worker-grpc.yaml`, rồi đổi server sang `AI_FACTORY_INFERENCE_MODE=worker` + `--inference-addr worker:50051`.
- **Secret dev-only** (`ai-factory-secrets`): đổi `jwt-secret`/`database-url` bằng overlay trước khi dùng thật.
