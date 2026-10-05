"""
gRPC Image Server — Python image worker entry point (process/port riêng).

Implements ImageService: nhận ImageRequest từ Go, trả ảnh (unary) hoặc stream
tiến độ từng step (server-streaming). Chạy process riêng vì GPU 12GB không chứa
đồng thời text model và SDXL (spec D3).

    python -m worker.image_server --engine sdxl --port 50052 --variant base
"""

import argparse
import asyncio
import signal
import sys
from concurrent import futures

import grpc

try:
    from .pb import image_pb2, image_pb2_grpc
except ImportError:
    print("[image_server] Proto stubs not found. Run: python -m worker.generate_proto")
    sys.exit(1)

DEFAULT_PORT = 50052


def _req_from_proto(request) -> dict:
    """Convert proto ImageRequest → dict cho backend."""
    return {
        "request_id": request.request_id,
        "prompt": request.prompt,
        "negative_prompt": request.negative_prompt,
        "width": request.width,
        "height": request.height,
        "steps": request.steps,
        "guidance_scale": request.guidance_scale,
        "seed": request.seed,
        "num_images": request.num_images,
        "scheduler": request.scheduler,
    }


def _response_from_result(result: dict) -> "image_pb2.ImageResponse":
    """Convert result dict của backend → proto ImageResponse."""
    return image_pb2.ImageResponse(
        png=result.get("png", []),
        width=result.get("width", 0),
        height=result.get("height", 0),
        seed=result.get("seed", 0),
        steps=result.get("steps", 0),
        scheduler=result.get("scheduler", ""),
    )


class ImageServiceServicer(image_pb2_grpc.ImageServiceServicer):
    """Implements the ImageService gRPC service."""

    def __init__(self, backend):
        self.backend = backend

    async def GenerateImage(self, request, context: grpc.aio.ServicerContext):
        req = _req_from_proto(request)
        print(f"[image_server] GenerateImage: request_id={req['request_id']} "
              f"size={req['width']}x{req['height']} steps={req['steps']} n={req['num_images']}")
        try:
            result = await self.backend.generate(req)
        except Exception as e:  # noqa: BLE001
            print(f"[image_server] Error in GenerateImage: {e}", file=sys.stderr)
            import traceback
            traceback.print_exc()
            await context.abort(grpc.StatusCode.INTERNAL, str(e))
        return _response_from_result(result)

    async def StreamImage(self, request, context: grpc.aio.ServicerContext):
        req = _req_from_proto(request)
        req_id = req["request_id"]
        print(f"[image_server] StreamImage: request_id={req_id} "
              f"size={req['width']}x{req['height']} steps={req['steps']}")
        try:
            async for item in self.backend.stream(req):
                if context.cancelled():
                    break
                result = item.get("result")
                progress = image_pb2.ImageProgress(
                    request_id=req_id,
                    step=item.get("step", 0),
                    total_steps=item.get("total_steps", 0),
                    done=item.get("done", False),
                    error=item.get("error", ""),
                )
                if result is not None:
                    progress.result.CopyFrom(_response_from_result(result))
                await context.write(progress)
        except Exception as e:  # noqa: BLE001
            print(f"[image_server] Error in StreamImage: {e}", file=sys.stderr)
            import traceback
            traceback.print_exc()
            try:
                await context.write(image_pb2.ImageProgress(
                    request_id=req_id, done=True, error=str(e),
                ))
            except Exception:  # noqa: BLE001 — context có thể đã đóng
                pass


async def serve(port: int = DEFAULT_PORT, engine: str = "sdxl", model_id: str | None = None,
                variant: str = "base", cpu_offload: bool = False, vae_tiling: bool = True,
                scheduler: str | None = None):
    """Start the gRPC image server."""
    print("[image_server] Starting AI Factory Image Worker...")
    print(f"[image_server] gRPC port: {port}, engine: {engine}, variant: {variant}")

    from .engines import get_image_backend
    backend = get_image_backend(engine, model_id=model_id, variant=variant,
                                cpu_offload=cpu_offload, vae_tiling=vae_tiling,
                                scheduler=scheduler)
    backend.load()

    server = grpc.aio.server(
        futures.ThreadPoolExecutor(max_workers=2),
        options=[
            ("grpc.max_send_message_length", 100 * 1024 * 1024),   # ảnh PNG
            ("grpc.max_receive_message_length", 10 * 1024 * 1024),
            ("grpc.keepalive_time_ms", 30000),
            ("grpc.keepalive_timeout_ms", 10000),
        ],
    )
    image_pb2_grpc.add_ImageServiceServicer_to_server(ImageServiceServicer(backend), server)
    server.add_insecure_port(f"[::]:{port}")

    await server.start()
    print(f"[image_server] Worker ready on port {port}. Waiting for Go server...")

    stop_event = asyncio.Event()

    def _signal_handler(sig, frame):  # noqa: ARG001
        print(f"\n[image_server] Received signal {sig}, shutting down...")
        stop_event.set()

    signal.signal(signal.SIGINT, _signal_handler)
    signal.signal(signal.SIGTERM, _signal_handler)

    await stop_event.wait()
    print("[image_server] Stopping...")
    await server.stop(5)
    backend.unload()
    print("[image_server] Image worker shut down.")


def main():
    """Entry point for python -m worker.image_server"""
    parser = argparse.ArgumentParser(description="AI Factory Image Worker")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help="gRPC port")
    parser.add_argument("--engine", type=str, default="sdxl", help="sdxl")
    parser.add_argument("--model", type=str, default=None,
                        help="Model ID (default: stabilityai/stable-diffusion-xl-base-1.0)")
    parser.add_argument("--variant", type=str, default="base", choices=["base", "turbo"],
                        help="base (25 step) | turbo (4 step)")
    parser.add_argument("--cpu-offload", action="store_true",
                        help="enable_model_cpu_offload (VRAM chật)")
    parser.add_argument("--no-vae-tiling", action="store_true",
                        help="tắt VAE tiling (mặc định bật)")
    parser.add_argument("--scheduler", type=str, default=None,
                        help="euler | dpmpp_2m (mặc định: pipeline default)")
    args = parser.parse_args()

    asyncio.run(serve(port=args.port, engine=args.engine, model_id=args.model,
                      variant=args.variant, cpu_offload=args.cpu_offload,
                      vae_tiling=not args.no_vae_tiling, scheduler=args.scheduler))


if __name__ == "__main__":
    main()
