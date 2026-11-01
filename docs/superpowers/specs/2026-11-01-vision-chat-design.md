# Design — Image input cho chat (Qwen2.5-VL-7B, engine `vl`)

- **Ngày**: 2026-11-01
- **Trạng thái**: ✅ Đã triển khai (proto + Python + Go + UI + tests)
- **Phạm vi**: thêm năng lực **hiểu ảnh** (image-text-to-text) vào chat: gửi ảnh qua `/v1/chat/completions` (OpenAI content parts), lưu ảnh, render lại trong lịch sử hội thoại. **Text pipeline giữ nguyên**: request text-only đi đúng đường cũ (proto `content` string, `TransformersBackend`/`LlamaBackend`, continuous batching).
- **Liên hệ**: nối tiếp `docs/superpowers/specs/2026-10-05-image-generation-sdxl-design.md` (image worker riêng, GPU độc quyền); cập nhật `docs/TRACKING.md`, `CLAUDE.md`, `CONTEXT.md`, `docs/worker-service.md`.

---

## 1. Bối cảnh & ràng buộc

### 1.1 Hiện trạng

- `proto/inference.proto` trước đây chỉ có `Message{role, content, tool_calls, ...}` — không có ảnh.
- Go `services/inference`: `OpenAIContent` chỉ nhận `string`; session `Message` không có ảnh; `Loop` map message → `infra.Message`.
- Python: `EngineBackend.generate/generate_batch` nhận text; `TransformersBackend` dùng forward pass tự viết + continuous batching; `LlamaBackend` proxy llama-server.
- Ảnh sinh ra (SDXL) đã có tiền lệ: file trên đĩa + endpoint auth `GET /api/v1/images/assets/:id` + fetch blob ở UI.

### 1.2 Ràng buộc VRAM (quyết định số 1)

RTX 3060 12GB: Qwen2.5-VL-7B 4-bit + vision tower bf16 chiếm ~7–8GB; Qwen3.5-9B GGUF ~5.7GB; SDXL ~9.6GB. **Chỉ một engine sở hữu GPU tại một thời điểm** — giống llama/SDXL, nên VL là **engine thứ ba của worker hiện có** (`--engine vl`), không phải process mới.

### 1.3 Vì sao không dùng continuous batching cho VL

Mỗi request có số ảnh/kích thước khác nhau → `pixel_values` phải pad rất nhiều, và forward pass tự viết (`worker/model/`) chỉ hiểu text. Vì vậy `VisionBackend` chạy thẳng `model.generate()` của transformers + `TextIteratorStreamer`, xử lý batch **tuần tự**.

---

## 2. Quyết định đã chốt

| # | Quyết định |
|---|---|
| **D1** | Chat với ảnh đi qua **`/v1/chat/completions`** với OpenAI content parts (`{type:"text"}` + `{type:"image_url",image_url:{url:"data:image/png;base64,..."}}`), không thêm endpoint vision riêng. |
| **D2** | Model **Qwen2.5-VL-7B-Instruct**, 4-bit NF4 (`llm_int8_skip_modules=["visual"]` giữ vision tower bf16), `attn_implementation="sdpa"`. Engine mới `--engine vl` trong worker hiện có. |
| **D3** | Proto: `Message` thêm `repeated Image images = 7`; `Image{string mime_type = 1; bytes data = 2;}`. `content` vẫn là string — ảnh là trường phụ. |
| **D4** | Text engine **từ chối** message có ảnh (`STOP_ERROR` "image input requires --engine vl") thay vì âm thầm bỏ ảnh — cả đường single lẫn batch. |
| **D5** | Ảnh lưu **content-addressed** trên đĩa (`workspace/images/uploads/<sha256>.png|.jpg`); DB `messages.images` (JSONB) chỉ lưu `[{id,mime_type,size}]`, **không** lưu base64. |
| **D6** | API phục vụ ảnh: `GET /api/v1/uploads/:id` (RequireAuth, `Cache-Control: private, max-age=31536000, immutable`). |
| **D7** | Chỉ nhận **inline `data:image/png|jpeg;base64,...`**; URL remote bị từ chối → không có bề mặt SSRF (server không fetch URL). |
| **D8** | Giới hạn: **2 MiB/ảnh** (`vision.max_image_bytes`), **4 ảnh/message** (`vision.max_images_per_message`). Client resize ≤1024px JPEG trước khi gửi. |
| **D9** | Ước lượng context: **1500 token/ảnh** (`imageTokenEstimate`) khi truncate/billing. |

---

## 3. Kiến trúc & luồng dữ liệu

```
Next.js /chat
  │ POST /v1/chat/completions  { messages:[{role:"user", content:[
  │      {type:"text",...}, {type:"image_url",image_url:{url:"data:image/jpeg;base64,..."}} ]}] }
  ▼
Go services/inference.Handler
  ├── OpenAIContent.UnmarshalJSON  → text + MessageImage{mime,data}
  ├── persistImages               → DiskImageStore.Save (sha256) → MessageImage{id,mime,size}
  ├── Session.AddMessage          → messages.images JSONB
  └── Loop.toInferenceMessages    → đọc lại bytes từ DiskImageStore (history) → infra.Message.Images
        │ gRPC Generate/BatchGenerate (limit 50 MiB)
        ▼
Python worker (--engine vl)
  ├── server.py map proto Image → dict {"images":[bytes], "image_mime_types":[...]}
  ├── VisionBackend.build_hf_messages → Qwen chat template (ảnh trước text)
  ├── AutoProcessor → pixel_values + input_ids
  └── model.generate + TextIteratorStreamer → token events → final(usage)
        │
        ▼
Go SSE → UI (text stream) ; ảnh user hiển thị từ data URL (turn vừa gửi)
                             hoặc AuthImage fetch /api/v1/uploads/:id (history)
```

### 3.1 Lưu trữ

- `DiskImageStore` (`services/inference/images.go`): `Save` validate mime + `image.DecodeConfig` + size cap, hash sha256, ghi temp → rename (atomic, idempotent theo nội dung). `Load(img)` theo mime đã biết; `LoadByID(id)` dò `.png`/`.jpg`. `validImageID` chỉ nhận 64 hex → chặn path traversal.
- Migration `0017_message_images`: thêm cột `messages.images JSONB`.
- GORM model `messageRow.Images []MessageImage` với `serializer:json`; `MessageImage.Data` có `json:"-"` nên bytes không bao giờ vào DB.

---

## 4. Thay đổi theo tầng

### 4.1 Proto (`proto/inference.proto`)

```proto
message Message {
  string role = 1;
  string content = 2;
  repeated ToolCall tool_calls = 3;
  string tool_call_id = 4;
  string tool_result = 5;
  bool is_error = 6;
  repeated Image images = 7;   // NEW
}

message Image {                 // NEW
  string mime_type = 1;         // image/png | image/jpeg
  bytes data = 2;               // raw bytes (Go nạp từ disk trước khi gửi)
}
```

Stub regenerate: Go `protoc -I proto --go_out=... --go-grpc_out=...`; Python `python -m worker.generate_proto`.

### 4.2 Python (`python-worker`)

- `worker/engines/base.py`: `EngineBackend.supports_vision = False` (class attr).
- `worker/engines/vision.py` (mới):
  - `decode_images` (base64 hoặc bytes → PIL RGB), `build_hf_messages` (map internal → Qwen template; ảnh đứng trước text; `tool` → user "Tool result: ..."), `sampling_kwargs` (greedy khi `temperature<=0`), `_CancelCriteria` (stopping criteria dừng `generate` khi request bị cancel).
  - `VisionBackend.generate/generate_batch`: batch tuần tự dưới `self._gen_lock`; generate trong thread riêng + streamer pump về `asyncio.Queue`; usage đếm prompt tokens từ `input_ids`, completion tokens bằng tokenizer.
  - Env: `AI_FACTORY_VL_MODEL` (default `Qwen/Qwen2.5-VL-7B-Instruct`), `AI_FACTORY_VL_QUANT` (`4bit`|`none`), `AI_FACTORY_VL_MIN_PIXELS`, `AI_FACTORY_VL_MAX_PIXELS` (default 1280·28²).
- `worker/engines/__init__.py`: đăng ký `"vl" → VisionBackend`.
- `worker/server.py`: map `m.images` → `{"images":[data...], "image_mime_types":[...]}`; guard `_messages_have_images` cho cả single và batch; CLI help thêm `vl`.

### 4.3 Go (`go-server`)

| File | Thay đổi |
|---|---|
| `infrastructure/inference/types.go`, `client.go` | `Message.Images []MessageImage`; map sang proto; `grpc.MaxCallSendMsgSize` 50 MiB |
| `services/inference/session.go` | `Message.Images []MessageImage`; `MessageImage{ID,MimeType,Size,Data(json:"-")}`; title fallback `"Image"`; `estimateMessageTokens` +1500/ảnh |
| `services/inference/adapters.go` | `OpenAIContent` custom `UnmarshalJSON` (string **hoặc** content parts); `parseImageDataURL` chỉ nhận `data:` + base64 + png/jpeg |
| `services/inference/images.go` | `ImageStore` + `DiskImageStore` (mới) |
| `services/inference/handler.go` | `NewHandler(..., images, maxImages, rpm, conc)`; `persistImages`; `handleGetUpload` |
| `services/inference/loop.go` | `NewLoop(scheduler, executor, images)`; `toInferenceMessages` nạp bytes từ store |
| `services/inference/router.go` | `GET /api/v1/uploads/:id` (RequireAuth) |
| `services/inference/models.go` + `store_gorm.go` | `images` JSONB đọc/ghi |
| `migrations/0017_message_images.go` | cột `messages.images` |
| `config/config.go`, `configs/config.yaml` | `VisionConfig{UploadsDir, MaxImageBytes, MaxImagesPerMessage}` |
| `app/registry.go`, `app.go`, `app/seeder.go` | DI `inference.images`; seed model `qwen2.5-vl-7b` + READY deployment; giá billing |

**Seed**: model `qwen2.5-vl-7b` / task `image-text-to-text` / framework `transformers`; template `transformers-vl`; deployment `qwen2.5-vl-7b-prod` (READY, không cần Kafka). Billing price mặc định như `qwen-3b` (150k/600k µcr per 1M input/output).

### 4.4 Web UI (`web`)

- `ChatClient.tsx`:
  - Nút đính kèm (paperclip) + input file ẩn (`accept="image/png,image/jpeg"`, multiple).
  - `downscaleImage`: `createImageBitmap` → canvas ≤1024px cạnh dài → `toDataURL("image/jpeg", 0.85)`.
  - Hàng thumbnail ảnh chờ gửi (nút ×); tối đa 4 ảnh; lỗi rõ ràng khi sai định dạng/quá lớn.
  - Tự chuyển sang model vision (tên chứa `-vl`) khi đính kèm ảnh lần đầu, tránh lỗi worker khó hiểu.
  - Gửi: có ảnh → content parts; không ảnh → string như cũ. Cho phép gửi ảnh không kèm text.
  - Lịch sử: map `messages[].images` → `AuthImage`.
- `AuthImage.tsx` (mới): fetch `/api/v1/uploads/:id` kèm Bearer → blob object URL (giống gallery SDXL).

---

## 5. Giới hạn & bảo mật

- **SSRF**: chỉ nhận `data:` URL; URL remote bị từ chối ở adapter.
- **Path traversal**: id là sha256 hex 64 ký tự do server sinh; `LoadByID` validate trước khi chạm filesystem.
- **Kích thước**: 2 MiB/ảnh server-side (`ErrImageTooLarge`), 4 ảnh/message; client resize để gần như luôn dưới ngưỡng.
- **Xác thực**: upload endpoint dùng `RequireAuth`; ảnh thuộc về hội thoại nên không public. Id là sha256 của nội dung, nên một user đã đăng nhập mà **biết** hash có thể đọc ảnh (hash không đoán được, nhưng đây không phải phân quyền theo tenant — chấp nhận cho phạm vi hiện tại).
- **Context/billing**: 1500 token ước lượng/ảnh → truncation và reserve không bị đánh giá thấp.
- **Tool calling**: `VisionBackend` bỏ qua `tools` (chưa hỗ trợ trên engine VL).

---

## 6. Cấu hình

```yaml
vision:
  uploads_dir: "workspace/images/uploads"
  max_image_bytes: 2097152       # 2 MiB
  max_images_per_message: 4
```

Env override: `AI_FACTORY_VISION_UPLOADS_DIR`, `AI_FACTORY_VISION_MAX_IMAGE_BYTES`, `AI_FACTORY_VISION_MAX_IMAGES_PER_MESSAGE`.

---

## 7. Vận hành

```bash
# 1) Tải model (chưa có sẵn)
huggingface-cli download Qwen/Qwen2.5-VL-7B-Instruct

# 2) Dừng worker text (GPU độc quyền), chạy worker VL
cd python-worker
python -m worker.server --engine vl --model Qwen/Qwen2.5-VL-7B-Instruct

# 3) Go server + web như cũ; model qwen2.5-vl-7b được seed READY
cd go-server && go run ./cmd/server/
cd web && npm run dev
```

- Muốn quay lại chat text: dừng worker VL, chạy `--engine transformers` (hoặc `llama`).
- Ảnh nằm ở `workspace/images/uploads/` (tương đối theo cwd của Go server); xoá thư mục = mất ảnh trong lịch sử (message vẫn còn, thumbnail trắng).
- NSSM: đổi `AppParameters` sang `--engine vl` (xem `docs/worker-service.md`).

---

## 8. Kiểm thử

- **Python** (`pytest tests/test_vision_backend.py`, CPU): `build_hf_messages` (system/tool/assistant/user, ảnh trước text, fallback text rỗng), `sampling_kwargs` (greedy/sample), `decode_images` (bytes + base64), `_CancelCriteria`, `supports_vision`. → 8 passed.
- **Go**:
  - `images_test.go`: Save/Load/LoadByID, dedup theo nội dung, reject (empty/unsupported/không phải ảnh/quá lớn), `ErrImageTooLarge`, missing → `ErrImageNotFound`, path traversal, `persistImages` (persist/drop bytes, replay, maxImages, không có store).
  - `adapters_test.go`: content string, content parts text+image, reject remote URL/thiếu url/mime lạ/không base64/base64 hỏng/rỗng/part lạ/không phải string|array, `OpenAIToInternal` mang ảnh.
  - `go build ./...`, `go vet ./...`, `go test ./...` xanh.
- **Web**: `npm run lint` + `npm run build` sạch.

---

## 9. Hạn chế đã biết

- Engine VL **không** dùng continuous batching/prefix cache/PagedAttention — batch tuần tự, throughput thấp hơn text engine.
- **Không tool calling** trên engine VL.
- Chưa có quota/billing riêng cho ảnh; ảnh tính theo ước lượng 1500 token.
- Chưa có endpoint xoá ảnh/GC file mồ côi (upload không được gửi vẫn nằm trên đĩa).
- Model VL và model text **không thể chạy đồng thời** trên 12GB — phải đổi engine thủ công.
