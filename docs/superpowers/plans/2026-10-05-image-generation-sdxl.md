# Text-to-Image (SDXL) — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** [`docs/superpowers/specs/2026-10-05-image-generation-sdxl-design.md`](../specs/2026-10-05-image-generation-sdxl-design.md) (D1–D10).

**Goal:** Thêm sinh ảnh text-to-image (SDXL 1.0, RTX 3060 12GB) vào AI Factory: image worker Python process riêng (gRPC :50052) → Go `services/image` (OpenAI `/v1/images/generations` + job API/SSE) → Next.js `/images`. Text pipeline bất biến. Staged: diffusers trước, tự viết diffusion sau.

**Tech:** Python 3.12 + `diffusers` + `accelerate` + `safetensors` + torch SDPA; Go Gin/GORM/gormigrate; Next.js App Router.

**Rollback tinh thần:** mỗi phase ship được độc lập; Phase 5 có cờ `AI_FACTORY_SELF_DIFFUSION` (default off).

---

## Decisions (locked)

- **D1** SDXL 1.0 base fp16, 1024px; variant Turbo/Lightning tuỳ chọn.
- **D2** Staged diffusers → tự viết.
- **D3** Image worker = process/port riêng (`:50052`); GPU độc quyền (dừng text worker).
- **D4** `/v1/images/generations` sync + job API + SSE tiến độ.
- **D5** `proto/image.proto` mới; `inference.proto` không đổi.
- **D6** Ảnh lưu disk `workspace/images/`; bảng `image_assets` + `image_jobs`.
- **D7** fp16 + SDPA + VAE tiling; `enable_model_cpu_offload` tuỳ chọn; batch=1.
- **D8** Billing/quota ảnh = Phase 6 (mặc định off/shadow).
- **D9** Config `image.*` + role `services.image`.
- **D10** Text pipeline bất biến.

## Global Constraints

- Chỉ image worker chạm GPU khi chạy. Không co-residence text+image trên 12GB.
- Go services **không import nhau**; port tiêu thụ nối ở `internal/app/adapters.go`.
- Test logic **CPU-only** (`tiny-stable-diffusion-xl-pipe` / tensor tổng hợp); GPU verify thủ công.
- TDD: test fail trước → implement → pass. `python -m pytest tests/` và `go test ./...` phải xanh.
- Không commit secret/prompt thô vào log.

---

## Phase 1 — Image data plane (Python)

**Files:** `proto/image.proto` (new), `python-worker/worker/generate_proto.py`, `worker/engines/image_base.py` (new), `worker/engines/sdxl.py` (new), `worker/image_server.py` (new), `worker/pb/image_pb2*.py` (gen), `python-worker/tests/test_sdxl_backend.py` (new), `pyproject.toml`.

- [x] Viết `proto/image.proto` (`ImageService.GenerateImage` unary + `StreamImage` server-stream; `ImageRequest`/`ImageResponse`/`ImageProgress`).
- [x] Mở rộng `generate_proto.py` compile `image.proto` + fix import relative; regenerate `worker/pb/`.
- [x] `worker/engines/image_base.py`: `ImageBackend` ABC (`load/unload/generate/stream`).
- [x] `worker/engines/sdxl.py`: `SDXLBackend` — load `StableDiffusionXLPipeline` fp16 (`variant="fp16"`), SDPA, `enable_vae_tiling()` (cờ), `enable_model_cpu_offload()` (cờ), `torch.inference_mode()`, seed qua `torch.Generator`, `num_images`, negative prompt, `callback_on_step_end` cho progress; `asyncio.Lock` guard.
- [x] `worker/image_server.py`: `ImageServiceServicer` (map proto↔backend, error → progress.error), `serve(port=50052)`, `main()` CLI `--engine sdxl --port 50052 --variant base|turbo`.
- [x] Tests CPU: tiny pipeline — PNG non-empty + đúng `width/height`, seed cố định → generator seed đúng, `num_images`, negative prompt truyền đúng, progress stream đủ `total_steps`, error path. (`tests/test_sdxl_backend.py`, `tests/test_image_server.py` — 16 tests)
- [x] `pyproject.toml`: thêm `diffusers>=0.32.0,<0.36`, `safetensors`, `Pillow` (ghim `<0.36` vì `>=0.36` kéo `huggingface-hub>=1.0`, xung đột `transformers 4.50`).
- [ ] GPU verify thủ công: 1024px fp16 không OOM, đo `s/ảnh`, xác nhận tiling. *(chưa chạy — cần tải weights ~7GB)*

## Phase 2 — Go control/API plane

**Files:** `internal/infrastructure/image/{client.go,pb/}` (new), `internal/services/image/*` (new), `internal/config/config.go`, `configs/config.yaml`, `internal/migrations/0015_image_assets.go` + `0016_image_jobs.go` (new), `internal/app/{registry,app,adapters,seeder}.go`, `cmd/{server,worker}/main.go`.

- [x] Regenerate Go pb cho `image.proto`; `internal/infrastructure/image/client.go` (`NewClient(addr)`, `Generate`, `Stream`).
- [x] Config: `ImageConfig{StorageDir,DefaultWidth,DefaultHeight,DefaultSteps,DefaultGuidance,MaxImages}` + `ServicesConfig.Image`; mặc định trong `config.Load()` + `configs/config.yaml`. (Inference addr theo pattern hiện có: `Options.ImageInferenceAddr` — flag `--image-inference-addr`.)
- [x] Flag `--image-inference-addr` (default `localhost:50052`) ở `cmd/server` → `app.Options`.
- [x] `internal/services/image`: `Service` (resolve deployment, build proto req, persist asset), `Handler` (OpenAI `/v1/images/generations` shape `{created,data:[{b64_json|url,revised_prompt,seed,width,height,steps}]}`; `size` → width/height; `n` ≤ `MaxImages`), `Router` (`RegisterRoutes`).
- [x] Ports tiêu thụ trong package image: `DeploymentResolver` (adapt serving ở `adapters.go`), `Generator` (adapt `*infraimage.Client`), `Storage` (DiskStorage); không import chéo service.
- [x] Migrations `0015_image_assets` + `0016_image_jobs`; đăng ký ở `internal/migrations/migrations.go`.
- [x] Seed: `sdxl-1.0` (task `text-to-image`, framework `diffusers`) + version `stabilityai/stable-diffusion-xl-base-1.0` + template `diffusers-sdxl` + READY deployment (idempotent, `seeder.go`).
- [x] Wiring `registry.go` (providers `image.client`/`image.service`/`http.image` gate `cfg.Services.API && cfg.Services.Image`), mount `http.image` ở `app.go:NewHTTPHandler`, force-resolve + `seedImage` ở `app.go`.
- [x] Tests Go: service defaults/validate/persist/model-not-found + handler OpenAI shape/url/invalid-size/404/auth (`internal/services/image/*_test.go`). `go test ./...` xanh.

## Phase 3 — Async jobs + storage

**Files:** `internal/services/image/{runner.go,storage.go,repository.go}` (new), `internal/app/{registry,app}.go`.

- [ ] `storage.go`: ghi/đọc `workspace/images/<asset_id>.png` (atomic write, path-traversal guard).
- [ ] `repository.go`: CRUD `image_jobs` (queued→running→succeeded/failed/cancelled), `image_assets`.
- [ ] `runner.go`: poll 5s (kiểu usage roller), chỉ `services.api`; guard terminal; cập nhật `step/total`; graceful shutdown theo context.
- [ ] SSE `GET /api/v1/images/jobs/:id/events` phát tiến độ (dùng `SSEWriter` sẵn có).
- [ ] Tests: runner idempotent + resume sau crash; storage path guard.

## Phase 4 — Web UI

**Files:** `web/next.config.ts` (rewrite `/v1/images/*`), `web/src/app/(app)/images/page.tsx` (new), `web/src/components/ImageGenerator.tsx` (new), `web/src/components/Sidebar.tsx`, `web/src/lib/types.ts`.

- [x] Proxy: rewrite `/v1/images/:path*` trong `next.config.ts` (JSON, không SSE nên buffering rewrite là đủ) + `/api/v1/*` đã có.
- [x] Trang `/images`: form prompt + negative + size preset + steps + guidance + seed; gọi `/v1/images/generations`; gallery kết quả + Recent (asset fetch kèm auth → object URL); nút tải PNG.
- [x] `Sidebar.tsx` thêm mục **Images**; `npm run lint` + `npm run build` xanh (route `/images` build tĩnh).

## Phase 5 — Tự viết diffusion (học, staged, cờ rollback)

**Files:** `worker/model/{scheduler,vae,unet,clip}.py` (new) + tests; `worker/engines/sdxl.py` (tích hợp cờ `AI_FACTORY_SELF_DIFFUSION`).

- [ ] 5a Scheduler + CFG (`scheduler.py`): Euler/DDIM + `eps = uncond + g*(cond-uncond)`; parity vs `pipe.scheduler` trên tensor tổng hợp.
- [ ] 5b VAE decode (`vae.py`): decoder conv + tiled; parity vs `pipe.vae.decode` (tiny, `allclose`).
- [ ] 5c UNet forward (`unet.py`): timestep embedding + ResNet + self/cross-attention (2D) + down/up; parity vs `pipe.unet` (tiny).
- [ ] 5d Text encoders (`clip.py`): CLIP-L + OpenCLIP-bigG text tower → `prompt_embeds`/`pooled`; parity vs `pipe.encode_prompt`.
- [ ] Cờ `AI_FACTORY_SELF_DIFFUSION` (default off), A/B benchmark self vs diffusers.

## Phase 6 — Billing/quota ảnh (tuỳ chọn)

**Files:** `internal/migrations/0017_model_image_pricing.go`, `internal/services/billing/*`, `internal/services/usage/*`, `internal/app/adapters.go`.

- [ ] Migration `model_pricing.price_per_image` (µcr/ảnh, NULL = không tính) + `billing.ReserveImage/SettleImage`.
- [ ] Port `ImageBillingGate` nối ở adapters; mode `off|shadow|enforce` (mặc định shadow).
- [ ] `QuotaEnforcer` hiểu prefix `images`; đếm từ `image_jobs` succeeded theo kỳ.

## Docs

- [ ] `CLAUDE.md` (architecture + Key Decisions), `docs/TRACKING.md`, `docs/LEARNING_ROADMAP.md`, `docs/ARCHITECTURE.md`, `docs/worker-service.md` (cách chạy/dừng image worker, GPU độc quyền).
- [x] Commit: `feat(image): SDXL text-to-image (worker + Go API + UI)`.

---

## Self-Review Checkpoints

- [ ] Text pipeline không đổi (proto/`server.py`/engine/`services/inference`).
- [ ] Go services không import chéo; port nối ở `adapters.go`.
- [ ] Test CPU-only xanh (`pytest`, `go test ./...`); GPU verify thủ công có số liệu.
- [ ] VRAM ≤ 12GB (batch=1) trên 3060; tiling/offload hoạt động.
- [ ] Docs vận hành ghi rõ GPU độc quyền (dừng text worker trước).

## Definition of Done

- `POST /v1/images/generations` sinh ảnh SDXL 1024px trả b64/url; job API + SSE hoạt động; UI `/images` dùng được.
- Image worker chạy như process riêng `:50052`; routing qua `sdxl-1.0` READY; migration/seed idempotent.
- Phase 5 parity (self-written vs diffusers) xanh trên CPU; cờ rollback.
- Docs + roadmap cập nhật.
