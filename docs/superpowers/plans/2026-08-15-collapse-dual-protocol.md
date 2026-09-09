# Gộp dual protocol về OpenAI /v1/chat/completions — Implementation Plan

> **Thực thi:** SDD (subagent-driven development) — 4 tasks, mỗi task implementer + task review (theo flow chuẩn project).

## Context

AI Factory hiện expose 2 dialect HTTP: **Anthropic `/v1/messages`** + **OpenAI `/v1/chat/completions`**, cùng normalize về internal canonical `session.Message` qua `adapters.go`. User đánh giá dual protocol là "tượng trưng" — không client thật nào phụ thuộc, chi phí thật: auth wrap cả 2 route (consumer slice vừa làm), UI/docs/tests đều mang cả 2 dialect. Đã chốt: **gộp về 1, giữ OpenAI `/v1/chat/completions`**, bỏ Anthropic.

**Nền tảng giữ nguyên (không đụng):** internal canonical `session.Message`; `SSEWriter`/`NewSSEWriter`/`SendError` (sse.go); `loop.RunStreaming`; gRPC + python-worker + proto (worker nói gRPC, dialect HTTP chỉ là client-facing). OpenAI streaming **đã implement đầy đủ & độc lập** (`handleOpenAIStream` phát `chat.completion.chunk` inline) — collapse là **xoá**, không viết lại.

## Quyết định

1. Xoá route `/v1/messages` hoàn toàn → **404** (không giữ stub).
2. Xoá mọi symbol Anthropic ở `adapters.go` + `handleAnthropic*` + `writeAnthropicError` + `mustMarshal` ở `handler.go` + `SendToken`/`SendToolUse`/`SendDone` ở `sse.go`.
3. **Prune kèm 2 function OpenAI dead** (đã không ai gọi, nằm trong file đang gọt): `InternalToOpenAIChoice`, `OpenAIToolsToInternal`.
4. `chat.html`: bỏ dropdown protocol + branch Anthropic, hardcode OpenAI — đồng thời **sửa luôn bug** anthropic-branch drop `systemPrompt`.
5. Docs: update **live docs** (README/CLAUDE/CONTEXT/ARCHITECTURE/TRACKING/BENCHMARK). **KHÔNG sửa** `docs/superpowers/specs|plans/*` (historical, point-in-time).
6. KHÔNG đụng bản go-server ở repo root (`G:\STUDY\AI\ai_factory\go-server\`) — đó là checkout branch main cũ; sẽ theo khi promote develop→main (đang deferred).

---

## Task 1: Go — collapse `adapters.go` + `sse.go` + `handler.go`

**Files:**
- `go-server/internal/api/adapters.go` — xoá: `AnthropicRequest`(23), `AnthropicMessage`(33), `AnthropicContent`(39), `AnthropicTool`(51), `AnthropicToInternal`(58), `InternalToAnthropicContent`(110, dead), `ToolsToInternal`(249, dead), `ValidateAnthropicRequest`(277) + header comment(14-16) + section "Anthropic Messages API → Internal"(19-20). **Prune dead:** `InternalToOpenAIChoice`(222), `OpenAIToolsToInternal`(263). Giữ: `OpenAIRequest`, `OpenAIMessage`, `OpenAIToolCall`, `OpenAIFunction`, `OpenAITool`, `OpenAIFunctionDef`, `OpenAIToInternal`, `ValidateOpenAIRequest`.
- `go-server/internal/api/sse.go` — xoá `SendToken`(39, emit Anthropic `content_block_delta`), `SendToolUse`(54, `tool_use`), `SendDone`(70, `message_stop`). Giữ `SSEWriter`+`NewSSEWriter`+`SendError`. (*handleOpenAIStream viết OpenAI SSE inline, chỉ dùng `sse.w`/`flusher` + `SendError`.*)
- `go-server/internal/api/handler.go` — xoá: route `mux.Handle("/v1/messages", ...)`(46), section banner(57), `handleAnthropicMessages`(60-114), `handleAnthropicStream`(116-181), `handleAnthropicNonStream`(183-266), `writeAnthropicError`(528-535), `mustMarshal`(549-552). Giữ nguyên OpenAI path (272-462) + `writeOpenAIError`. Imports: `uuid` vẫn dùng (331, 438); `go build` sẽ bắt import orphan.

**Verify Task 1:** `cd go-server && go build ./...` + `go test ./...` xanh. Grep trong `go-server`: `rg -i "anthropic|v1/messages" internal cmd` → chỉ còn comment generated `pb/inference.pb.go:226` (giữ, cosmetic — xoá đòi regenerate proto, bỏ qua).

## Task 2: Go — main.go log + test path

**Files:**
- `go-server/cmd/server/main.go` — xoá log line `"  Anthropic: POST http://localhost:%d/v1/messages"`(131); sửa comment `statusRecorder.Flush`(192) chỉ còn `/v1/chat/completions`.
- `go-server/internal/auth/middleware_test.go` — đổi path `httptest.NewRequest(http.MethodPost, "/v1/messages", nil)`(106) → `"/v1/chat/completions"` (test chỉ exercise middleware, assertion giữ nguyên).
- Không có test nào khác assert Anthropic (controlplane_test.go chỉ dùng `/v1/chat/completions`; handler_test.go chỉ UI routing).

**Verify Task 2:** `go build ./... && go test ./...` xanh; `go vet ./...`.

## Task 3: UI — chat.html + concepts.html

**Files:**
- `ui/chat.html`:
  - Badge `#protocol-badge`(258): "Anthropic" → "OpenAI".
  - Xoá `<select id="protocol">`(259-262) + onchange handler(599-602).
  - `send()`: xoá `const protocol`(636) + branch Anthropic(656-663); giữ nguyên OpenAI branch(665-675) thành đường duy nhất. *(Sửa luôn bug drop `systemPrompt` ở branch Anthropic.)*
  - SSE parse: xoá branch Anthropic(710-720), giữ OpenAI(721-733).
  - Help text(766): "Chọn protocol…" → ghi chú gọi `/v1/chat/completions` (OpenAI).
- `ui/concepts.html`: cập nhật 4 điểm — subtitle(608) "Dual protocol" → "OpenAI-compat"; mô tả API server(633) bỏ `/v1/messages`; callout tool format(894) chỉ còn `tool_calls` OpenAI; roadmap(1026) bỏ "Dual protocol Anthropic + OpenAI".

**Verify Task 3:** không phá JS (mở file, không còn ref `protocol`/`anthropic`); `rg -i "anthropic|v1/messages" ui/` → sạch.

## Task 4: Docs — live docs + grep sweep toàn repo

**Files** (chỉ sửa các dòng liệt kê, đổi thành OpenAI-only):
- `README.md`: 3, 9, 22, 65-68 (curl mẫu → `/v1/chat/completions` body đơn giản), 102.
- `CLAUDE.md`: 78 (arch tree), 100 (project structure "dual protocol"), 136 (Key Decisions "Dual protocol"), 158 (roadmap), 172 (Known Gaps auth — bỏ `/v1/messages`), 191-199 (Running curl mẫu → `/v1/chat/completions`, content là string không còn NOTE array-blocks).
- `CONTEXT.md`: 9, 51, 52, 78.
- `docs/ARCHITECTURE.md`: 15, 30-31, 67, 105-106, 112, 116, 122, 128, 316-331 (section 5.1 viết lại thành OpenAI non-stream), 474-478, 489.
- `docs/TRACKING.md`: 17, 29, 38, 46, 111.
- `docs/BENCHMARK.md`: 173, 175.

**Verify Task 4:** grep sweep toàn repo (trừ `docs/superpowers/specs|plans` + bản repo root + `.git`):
`rg -i "anthropic|/v1/messages" --glob '!docs/superpowers/**' --glob '!go-server/internal/inference/pb/**' .` → sạch.

---

## Verification cuối (sau 4 tasks)

1. `cd go-server && go vet ./... && go test ./...` — toàn bộ xanh (DB-gated skip nếu không Postgres).
2. Restart server `go run ./cmd/server/ --port 18080`; curl:
   - `POST /v1/messages` → **404**.
   - `POST /v1/chat/completions` (Bearer JWT, `stream:true`) → **200 `text/event-stream`**, event `chat.completion.chunk` + `[DONE]` (không còn lỗi SSE; nếu worker llama không chạy thì lỗi `Unavailable` trong event — đúng).
   - no-auth `POST /v1/chat/completions` → **401**.
   - `GET /` `/chat` `/keys` → 3 trang vẫn đúng title.
3. Browser flow: login `admin/admin1234` → `/chat` gửi tin (dropdown protocol đã biến mất, body OpenAI) → tool-use qua worker llama nếu có.

## Lưu ý

- Bản go-server repo root (main cũ) vẫn còn `/v1/messages` không auth — sẽ được thay khi promote develop→main (deferred theo user).
- Không sinh spec doc riêng: quyết định + rationale được ghi vào `ARCHITECTURE.md`/`CLAUDE.md` trong Task 4.
