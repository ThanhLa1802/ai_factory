# Design — Text-to-Image (SDXL 1.0) trên RTX 3060 12GB

- **Ngày**: 2026-10-05
- **Trạng thái**: Draft — chờ user duyệt spec trước khi lập plan chi tiết (đã chốt các quyết định D1–D10 bên dưới)
- **Phạm vi**: thêm năng lực **sinh ảnh** (text-to-image) vào AI Factory: data plane (Python worker + diffusers), control/API plane (Go), UI (Next.js). Model chính **SDXL 1.0**. **Text pipeline (proto `inference.proto`, engine `transformers`/`llama`, Go `services/inference`) không đổi.**
- **Liên hệ**: nối tiếp kiến trúc modular monolith (`docs/superpowers/specs/2026-09-11-modular-monolith-rearchitecture-design.md`); theo cùng triết lý học self-written của `docs/superpowers/specs/2026-09-18-self-written-forward-pass-design.md`; cập nhật `docs/TRACKING.md`, `docs/LEARNING_ROADMAP.md`.

---

## 1. Bối cảnh & ràng buộc (đã xác minh từ code)

### 1.1 Hiện trạng — text-only

- `proto/inference.proto`: 2 service server-streaming `InferenceService.Generate` (single) + `BatchInferenceService.BatchGenerate` (batch). Không có message/ảnh.
- Go: `internal/services/inference` (handler `/v1/chat/completions`, SSE) → `internal/infrastructure/inference/batch_scheduler.go` → gRPC `Client` (`client.go`, dial `Options.InferenceAddr`, default `localhost:50051`).
- Python: `worker/server.py` (gRPC) + `worker/engines/{transformers,llama}` theo `EngineBackend` (`engines/base.py`). `Engine Concurrency Guard` (một `asyncio.Lock`) bảo đảm một forward pass tại một thời điểm.
- Control plane: `serving.Model` đã có cột `task` (migration `0001_init.go:61`) + `framework`; routing `ResolveDeployment(tenantID, modelName)` join `deployments→model_versions→models`, lọc `status=READY`, sắp theo `created_at DESC` (`repository_serving.go:242`). Seed hiện tại: model `qwen3.5-9b` / task `text-generation` / framework `llama.cpp` (`internal/app/seeder.go:101`).
- Composition root: `internal/app/registry.go` (DI providers), `adapters.go` (consumer-defined ports: `DeploymentResolver`, `BillingGate`, `QuotaGate`, `UsageRecorder`), `app.go` mount router qua interface `RegisterRoutes(*gin.Engine)`.
- Config phẳng (`internal/config/config.go`) + viper (`configs/config.yaml`, env `AI_FACTORY_*`), role flags `services.api|worker|billing`.

### 1.2 Ràng buộc VRAM — **quyết định kiến trúc số 1**

| Thành phần | VRAM (fp16) |
|---|---|
| Qwen3.5-9B Q4_K_M (text worker) | ~5.7 GB |
| SDXL 1.0 đỉnh (1024px, fp16, UNet+2 text encoder+VAE) | ~9.6 GB |
| FLUX.1-dev fp8 (tham chiếu) | ~11.6 GB (sát trần, chậm) |

→ Text model và image model **không thể cùng nằm trong 12GB**. Vì vậy image model **phải chạy ở process riêng, sở hữu GPU độc quyền** khi text worker dừng (và ngược lại). Đây là lý do D3.

### 1.3 Chọn model cho RTX 3060 12GB

| Model | VRAM đỉnh (1024px) | Thời gian/ảnh | License | Ghi chú |
|---|---|---|---|---|
| **SDXL 1.0 base** ✅ | ~9.6 GB (fp16) | 14–22 s | OpenRAIL++-M (permissive) | Native 1024px, ecosystem LoRA/ControlNet lớn nhất, diffusers hỗ trợ tốt |
| SD 1.5 | ~3.8 GB | 3–8 s | CreativeML OpenRAIL-M | Nhẹ/nhanh nhưng native 512px, chất lượng thấp hơn |
| FLUX.1-schnell (GGUF Q4/NF4) | ~8–9 GB | 30–45 s (4 step) | Apache-2.0 | Chất lượng cao nhất, chữ trong ảnh tốt; dependency nặng (GGUF loader / quantize), chậm |
| Sana 1.6B | ~5–6 GB | nhanh | NVIDIA non-commercial* | Mới, ít tooling/LoRA |

**Chốt: SDXL 1.0 base** (`stabilityai/stable-diffusion-xl-base-1.0`), fp16. Đây là điểm cân bằng tốt nhất cho 12GB: vừa khít có headroom, ecosystem phong phú, diffusers là đường "black-box" chuẩn rồi tiến tới tự viết. Option nhanh để lặp prompt: **SDXL-Turbo** (1–4 step) hoặc **SDXL-Lightning** LoRA — cấu hình `image.variant`, mặc định tắt.

---

## 2. Quyết định đã chốt (brainstorming với user)

| # | Quyết định |
|---|---|
| **D1** | **Model chính = SDXL 1.0 base**, fp16, native 1024×1024. Variant nhanh (Turbo/Lightning) là tuỳ chọn cấu hình, không mặc định. |
| **D2** | **Staged**: Phase 1–4 dùng `diffusers` như black-box để ship tính năng; Phase 5 lần lượt **tự viết** scheduler+CFG → VAE decode → UNet forward → text encoder (kèm parity test CPU tiny), song song triết lý self-written của text pipeline. |
| **D3** | **Image worker là process/port riêng** (`python -m worker.image_server --engine sdxl`, gRPC port **50052**). Go dial qua `--image-inference-addr` / `AI_FACTORY_IMAGE_INFERENCE_ADDR`. GPU chỉ một trong hai (text/image) chạy tại một thời điểm — ghi rõ trong docs vận hành. |
| **D4** | **API surface**: OpenAI-compatible `POST /v1/images/generations` (sync, trả `b64_json` hoặc `url`) **+ job API bất đồng bộ** (`POST/GET /api/v1/images/jobs`, SSE tiến độ) cho ảnh lâu. |
| **D5** | **Proto mới `proto/image.proto`** (package `image`, `go_package = go-server/internal/infrastructure/image/pb`): `ImageService.GenerateImage` (unary) + `ImageService.StreamImage` (server-stream tiến độ). `inference.proto` **không đổi**. |
| **D6** | **Lưu ảnh**: ghi PNG ra đĩa (`workspace/images/<id>.png`), metadata trong bảng `image_assets`; job trong `image_jobs`. Endpoint phục vụ file `GET /api/v1/images/assets/:id` (auth). |
| **D7** | **Chiến lược VRAM**: fp16 + `torch.inference_mode()` + attention SDPA; `enable_vae_tiling()` cho >1024px; fallback `enable_model_cpu_offload()` khi cần; **batch = 1**. Cờ `image.cpu_offload`, `image.vae_tiling`. |
| **D8** | **Billing/quota cho ảnh = phase sau** (Phase 6, tuỳ chọn): thêm chiều giá `price_per_image` (µcr/ảnh) và quota type prefix `images…`. Mặc định `off/shadow`; **không** chặn Phase 1–4. |
| **D9** | **Cấu hình**: thêm `image.inference_addr`, `image.storage_dir`, `image.default_{width,height,steps,guidance,max_images}`, `image.{cpu_offload,vae_tiling,sdpa,variant}`; role flag `services.image` (gate HTTP routes + job runner trên API node). |
| **D10** | **Text pipeline bất biến**: `inference.proto`, `worker/server.py`, `engines/transformers|llama`, `services/inference`, scheduler đều **không sửa**. Chỉ thêm provider mới. |

---

## 3. Kiến trúc

```
Client / Next.js /images
   │  POST /v1/images/generations   (sync, OpenAI)
   │  POST /api/v1/images/jobs      (async) ── GET /:id, SSE /:id/events
   ▼
Go API node
   ├── internal/services/image  (handler/router/service, job runner)
   │      ├── ImageResolver ─────► serving.ResolveDeployment (task=text-to-image)
   │      ├── ImageStorage        (disk + image_assets)
   │      └── (Phase 6) Billing/Quota ports
   └── internal/infrastructure/image.Client
          │  gRPC GenerateImage / StreamImage
          ▼
Python image worker  (process riêng, GPU độc quyền, :50052)
   worker/image_server.py  ── ImageService servicer
   worker/engines/sdxl.py  ── SDXLBackend (diffusers StableDiffusionXLPipeline)
                                  │
                    [Phase 5] thay dần: scheduler+CFG → VAE → UNet → text encoders
```

Bất biến:
- **Chỉ image worker chạm GPU khi chạy** (text worker phải dừng). Ghi nhận trong `docs/worker-service.md`.
- Go services **không import nhau** — image service định nghĩa port riêng, adapter nối ở `internal/app/adapters.go` (đúng D-P4-2).
- Một `ImageAsset`/`ImageJob` gắn `tenant_id`; auth qua `InferenceAuth`/`RequireAuth` + RBAC.

---

## 4. Thiết kế chi tiết theo phase

### Phase 1 — Image data plane (Python)

**`proto/image.proto`** (mới):

```proto
syntax = "proto3";
package image;
option go_package = "go-server/internal/infrastructure/image/pb";

service ImageService {
  rpc GenerateImage(ImageRequest) returns (ImageResponse);
  rpc StreamImage(ImageRequest) returns (stream ImageProgress);
}

message ImageRequest {
  string request_id = 1;
  string prompt = 2;
  string negative_prompt = 3;
  int32 width = 4;            // default 1024
  int32 height = 5;           // default 1024
  int32 steps = 6;            // default 25
  float guidance_scale = 7;   // default 7.0
  int64 seed = 8;             // -1 => random
  int32 num_images = 9;       // default 1
  string scheduler = 10;      // euler | dpmpp_2m
}

message ImageResponse {
  repeated bytes png = 1;
  int32 width = 2; int32 height = 3;
  int64 seed = 4; int32 steps = 5; string scheduler = 6;
}

message ImageProgress {
  string request_id = 1;
  int32 step = 2; int32 total_steps = 3;
  bool done = 4;
  ImageResponse result = 5;   // set khi done
  string error = 6;
}
```

- Mở rộng `worker/generate_proto.py` để compile cả `image.proto` (fix import relative tương tự `inference_pb2_grpc.py`).
- `worker/engines/sdxl.py` — `SDXLBackend` (mới, không thừa kế `EngineBackend`; định nghĩa `ImageBackend` ABC riêng trong `worker/engines/image_base.py`): build `StableDiffusionXLPipeline.from_pretrained(..., torch_dtype=torch.float16, variant="fp16")`, `to("cuda")`, `enable_attention_slicing`/SDPA, `enable_vae_tiling()` theo cờ, `enable_model_cpu_offload()` theo cờ; `generate(req) -> ImageResponse` (dùng `torch.Generator` seed; lặp `num_images`); `stream(req)` phát `ImageProgress` qua `callback_on_step_end`. Guard `asyncio.Lock` như text.
- `worker/image_server.py` — `ImageServiceServicer` + `serve(port=50052, ...)` + `main()` CLI `--engine sdxl --port 50052 --variant base|turbo`.
- Tests CPU: tiny pipeline (`hf-internal-testing/tiny-stable-diffusion-xl-pipe` hoặc tiny SD) — shape PNG, seed reproducibility, num_images, negative prompt, progress stream, error path. GPU verify thủ công (OOM/seconds) ngoài CI.

### Phase 2 — Go control/API plane

- `internal/infrastructure/image/client.go` — `Client` dial `ImageInferenceAddr`, `GenerateImage(ctx, req)`, `StreamImage(ctx, req)`.
- `internal/services/image/`:
  - `handler.go` / `router.go` — `RegisterRoutes(*gin.Engine)`:
    - `POST /v1/images/generations` (`InferenceAuth`) — OpenAI body `{model,prompt,negative_prompt,n,size,steps,guidance_scale,seed,response_format}`; trả `{created,data:[{b64_json|url,revised_prompt}]}`.
    - `POST /api/v1/images/jobs` (`RequirePermission("inference.use")`), `GET /api/v1/images/jobs`, `GET /api/v1/images/jobs/:id`, `GET /api/v1/images/jobs/:id/events` (SSE), `GET /api/v1/images/assets/:id`.
  - `service.go` — resolve deployment (task `text-to-image`), build gRPC request, persist asset/job, phát progress.
  - ports tiêu thụ: `ImageResolver`, `ImageStorage`, `JobRepository` (định nghĩa trong package image).
- Config (`internal/config/config.go` + `configs/config.yaml`): `ImageConfig` + `ServicesConfig.Image`.
- Migrations `0015_image_assets`, `0016_image_jobs` (schema: id, tenant_id, deployment_id, model, prompt, negative_prompt, width, height, steps, guidance, seed, path/png, status, error, timestamps). Pattern `NNNN_snake.go`.
- Seed: `findOrCreateModel(Name:"sdxl-1.0", Task:"text-to-image", Framework:"diffusers")` + version artifact `stabilityai/stable-diffusion-xl-base-1.0` + template `diffusers-sdxl` + READY deployment cho demo tenant.
- Wiring: `registry.go` thêm providers (`image.client`, `image.service`, `http.image`) gate `cfg.Services.API && cfg.Services.Image`; `app.go` mount 1 dòng; `adapters.go` thêm `imageDeploymentResolver` (tái dùng serving) + `imageStorage` adapter.
- Tests: handler (OpenAI shape, auth, 404 model, size mapping), service (resolve/persist), client (fake gRPC).

### Phase 3 — Async jobs + storage

- `internal/services/image/runner.go` — background runner (5s poll kiểu usage roller, chỉ `services.api`): nhận job `queued` → gọi image client → ghi file + cập nhật job `succeeded/failed`; idempotent (guard trạng thái terminal). Progress cập nhật `step/total`.
- `storage.go` — ghi/đọc `workspace/images/<asset_id>.png` (path traversal guard, atomic write). Cấu hình `image.storage_dir`.
- Graceful shutdown: runner dừng theo context như roller.

### Phase 4 — Web UI

- `web/src/app/v1/images/generations/route.ts` — proxy POST → Go (mirror `v1/chat/completions/route.ts`).
- `web/src/app/(app)/images/page.tsx` + `web/src/components/ImageTab.tsx`/`ImageGenerator.tsx` — form (prompt, negative, size preset, steps, guidance, seed, n), gallery, hiển thị tiến độ (poll job / SSE), nút tải ảnh.
- `Sidebar.tsx` thêm mục **Images**; hiển thị đúng khi model `sdxl-1.0` READY.

### Phase 5 — Tự viết diffusion (học, staged, mỗi bước có parity test)

5a. **Scheduler + CFG** (`worker/model/scheduler.py`): Euler / DDIM, `sigma`/`alpha` schedule, `step(noise_pred, t, sample)`; CFG `eps = uncond + g*(cond-uncond)`. Parity vs `pipe.scheduler` trên tensor tổng hợp.
5b. **VAE decode** (`worker/model/vae.py`): decoder conv + attention + tiled decode; parity vs `pipe.vae.decode` (tiny).
5c. **UNet forward** (`worker/model/unet.py`): timestep embedding, ResNet blocks, self/cross-attention (2D), down/up samples; parity vs `pipe.unet` (tiny, `allclose`).
5d. **Text encoders** (`worker/model/clip.py`): CLIP-L + OpenCLIP-bigG text tower (reuse leaf modules / hand RoPE-free attention), produce `prompt_embeds`/`pooled`; parity vs `pipe.encode_prompt`.
Cờ rollback `AI_FACTORY_SELF_DIFFUSION` (default off cho đến khi parity xanh), tương tự `AI_FACTORY_SELF_FORWARD`.

### Phase 6 — Billing/quota cho ảnh (tuỳ chọn)

- Migration thêm `model_pricing.price_per_image` (µcr/ảnh, `NULL` = không tính) + `billing.ReserveImage/SettleImage`; port `ImageBillingGate` nối ở adapters.
- `QuotaEnforcer` hiểu prefix `images`; đếm từ `image_jobs` succeeded theo kỳ. Mode `off|shadow|enforce` như text.

---

## 5. Testing

- **CPU-only** cho toàn bộ test logic (tiny diffusers pipeline / tensor tổng hợp). Không cần GPU trong CI.
- **Parity là tiêu chí #1** ở Phase 5 (self-written vs diffusers, `torch.allclose` trên tiny model; GPU so `max abs diff`).
- Python: `python -m pytest tests/` (env `mywork`) — phải xanh trước/sau mỗi phase.
- Go: `go test ./...` + `go vet`; web: `npm run build`/lint.
- **GPU verify thủ công (ngoài CI)**: SDXL 1024px fp16 trên 3060 12GB — không OOM, đo `s/ảnh`, seed reproducibility, VAE tiling.
- Benchmark: mở rộng `python-worker/benchmark.py` chế độ image (steps vs thời gian, VRAM đỉnh).

## 6. Risks / open questions

- **VRAM 12GB**: SDXL fp16 đỉnh ~9.6GB, còn ~2.4GB; bật LoRA/ControlNet/batch>1 dễ OOM → mặc định batch=1, tiling, offload fallback. Đo thực tế trước khi hứa "concurrent".
- **GPU độc quyền**: quên dừng text worker → OOM. Cần ghi rõ + (tương lai) GPU broker/health-check chéo.
- **Độ trễ HTTP sync**: 14–22s/ảnh có thể vượt timeout client/proxy → job API là đường chính cho ảnh lâu.
- **diffusers/transformers version drift**: SDXL cần `diffusers>=0.27` + `accelerate` + `safetensors`; ghim version trong `pyproject.toml`.
- **License**: SDXL base OpenRAIL++-M (dùng thương mại được, có điều kiện); ghi rõ trong UI/docs.
- **Phase 5 quy mô**: UNet forward rất lớn (conv 2D + attention) — có thể chỉ hoàn thành 5a/5b trong ngắn hạn; 5c/5d là mục tiêu dài hạn, giữ cờ rollback.

## 7. Non-goals

- ❌ Không đổi text pipeline (`inference.proto`, `server.py`, `engines/transformers|llama`, `services/inference`).
- ❌ Không train/fine-tune (LoRA training), không ControlNet/IP-Adapter, không inpainting/editing (`/v1/images/edits`), không image-to-image trong scope này.
- ❌ Không multi-GPU, không co-residence text+image trên 12GB.
- ❌ Không CUDA kernel tự viết (Phase 5 dùng PyTorch ops).

## 8. Files touched (dự kiến)

**Phase 1**: `proto/image.proto` (MỚI), `python-worker/worker/generate_proto.py`, `worker/engines/image_base.py` (MỚI), `worker/engines/sdxl.py` (MỚI), `worker/image_server.py` (MỚI), `worker/pb/image_pb2*.py` (gen), `python-worker/tests/test_sdxl_backend.py` (MỚI), `pyproject.toml`.
**Phase 2**: `go-server/proto` (gen) → `internal/infrastructure/image/client.go` (MỚI) + `pb/`, `internal/services/image/*` (MỚI), `internal/config/config.go`, `configs/config.yaml`, `internal/migrations/{0015,0016}_*.go` (MỚI), `internal/app/{registry,app,adapters,seeder}.go`, `cmd/server/main.go`, `cmd/worker/main.go`.
**Phase 3**: `internal/services/image/{runner,storage,repository}.go` (MỚI).
**Phase 4**: `web/src/app/v1/images/generations/route.ts` (MỚI), `web/src/app/(app)/images/page.tsx` (MỚI), `web/src/components/ImageGenerator.tsx` (MỚI), `web/src/components/Sidebar.tsx`.
**Phase 5**: `worker/model/{scheduler,vae,unet,clip}.py` (MỚI) + tests.
**Phase 6**: `internal/migrations/0017_*.go`, `internal/services/billing/*`, `internal/services/usage/*`, `internal/app/adapters.go`.
**Docs**: `CLAUDE.md`, `docs/TRACKING.md`, `docs/LEARNING_ROADMAP.md`, `docs/ARCHITECTURE.md`, `docs/worker-service.md`.
