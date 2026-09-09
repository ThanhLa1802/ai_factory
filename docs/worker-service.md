# Worker service (NSSM)

Worker inference chạy tự động như Windows service `AIFactoryWorker` (engine llama, Qwen3.5-9B GGUF).

`nssm` đặt tại `C:\Users\thanh\bin\nssm.exe` (thư mục `C:\Users\thanh\bin` đã được thêm vào PATH người dùng).

## Quản lý service

```powershell
nssm status  AIFactoryWorker            # trạng thái
nssm restart AIFactoryWorker            # restart
nssm stop    AIFactoryWorker            # dừng tạm
nssm start   AIFactoryWorker            # chạy lại
nssm remove  AIFactoryWorker confirm    # gỡ service
# hoặc mở services.msc
```

### Dừng để tiết kiệm tài nguyên

```powershell
nssm stop AIFactoryWorker
# worker spawn llama-server (chiếm ~5.7GB VRAM) làm process con — kill nốt nếu còn:
Get-Process llama-server -ErrorAction SilentlyContinue | Stop-Process -Force
```

Muốn dừng + **không tự chạy lại khi reboot** (vì Start=AUTO):

```powershell
nssm set AIFactoryWorker Start SERVICE_DEMAND_START   # chỉ chạy khi gọi thủ công
nssm stop AIFactoryWorker
```

Bỏ hẳn: `nssm remove AIFactoryWorker confirm`.

## Cấu hình hiện tại

| Mục | Giá trị |
|---|---|
| Lệnh | `C:\Users\thanh\anaconda3\envs\mywork\python.exe -u -m worker.server --engine llama --gguf G:\models\Qwen3.5-9B-Q4_K_M.gguf --llama-bin G:\models\llama.cpp\llama-server.exe` |
| AppDirectory | `G:\STUDY\AI\ai_factory\python-worker` |
| Start | `SERVICE_AUTO_START` (tự chạy khi boot) |
| AppExit | `Default = Restart` (tự restart khi crash, delay 5s) |
| Account | LocalSystem |

## Log

- `python-worker\worker-service.log` — stdout worker
- `python-worker\worker-service.err.log` — stderr worker
- `python-worker\llama-server-8081.log` — llama-server

## Đổi engine

```powershell
# Về transformers:
nssm set AIFactoryWorker AppParameters "-u -m worker.server --engine transformers"
nssm restart AIFactoryWorker

# Về llama (nhớ --gguf + --llama-bin đầy đủ):
nssm set AIFactoryWorker AppParameters "-u -m worker.server --engine llama --gguf G:\models\Qwen3.5-9B-Q4_K_M.gguf --llama-bin G:\models\llama.cpp\llama-server.exe"
nssm restart AIFactoryWorker
```

## Cài lại từ đầu

Script: `C:\Users\thanh\bin\setup-worker-service.ps1` (chạy elevated — sửa `$appArgs` nếu muốn đổi engine/đường dẫn rồi chạy lại).
