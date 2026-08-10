"""
gRPC Inference Server — Python worker entry point.

Implements InferenceService: nhận GenerateRequest từ Go, stream GenerateResponse về.
"""

import asyncio
import json
import signal
import sys
from concurrent import futures
from pathlib import Path

import grpc

# Import generated proto stubs (run generate_proto.py first if missing)
try:
    from .pb import inference_pb2, inference_pb2_grpc
except ImportError:
    print("[server] Proto stubs not found. Run: python -m worker.generate_proto")
    sys.exit(1)

# Servicers nhận EngineBackend (worker.engines) thay vì trực tiếp engine.

# ---------------------------------------------------------------------------
# gRPC Service Implementation
# ---------------------------------------------------------------------------

DEFAULT_PORT = 50051


class InferenceServicer(inference_pb2_grpc.InferenceServiceServicer):
    """Implements the InferenceService gRPC service."""

    def __init__(self, backend):
        self.backend = backend

    async def Generate(self, request: inference_pb2.GenerateRequest, context: grpc.aio.ServicerContext):
        """Handle Generate RPC — server-streaming tokens back to Go."""
        request_id = request.request_id
        session_id = request.session_id
        print(f"[server] Generate: request_id={request_id}, session_id={session_id}, "
              f"messages={len(request.messages)}, max_tokens={request.sampling_params.max_tokens}")

        # Convert proto → dicts for engine
        messages = _messages_from_proto(request.messages)
        tools = _tools_from_proto(request.tools) if request.tools else None
        system_prompt = request.system_prompt if request.system_prompt else None

        sampling_params = {
            "max_tokens": request.sampling_params.max_tokens or 1024,
            "temperature": request.sampling_params.temperature or 0.7,
            "top_p": request.sampling_params.top_p or 0.9,
            "top_k": request.sampling_params.top_k or 50,
        }
        if request.sampling_params.stop_sequences:
            sampling_params["stop_sequences"] = list(request.sampling_params.stop_sequences)

        # Prepend system prompt as a message if provided
        engine_messages = []
        if system_prompt:
            engine_messages.append({"role": "system", "content": system_prompt})
        engine_messages.extend(messages)

        # Create cancel event that watches gRPC context
        cancel_event = asyncio.Event()

        async def watch_cancel():
            """Monitor gRPC context for cancellation from Go side."""
            while not context.cancelled():
                await asyncio.sleep(0.1)
            cancel_event.set()
            print(f"[server] Request {request_id} cancelled by client")

        cancel_task = asyncio.create_task(watch_cancel())

        try:
            # Stream tokens
            async for event in self.backend.generate(
                messages=engine_messages,
                sampling_params=sampling_params,
                tools=tools,
                cancel_event=cancel_event,
            ):
                if context.cancelled():
                    break

                response = _build_response(event)
                await context.write(response)

        except Exception as e:
            print(f"[server] Error in Generate: {e}", file=sys.stderr)
            import traceback
            traceback.print_exc()
            error_response = inference_pb2.GenerateResponse(
                event_type=inference_pb2.EVENT_FINAL,
                stop_reason=inference_pb2.STOP_ERROR,
                finish_reason="error",
                token=str(e),
            )
            await context.write(error_response)
        finally:
            cancel_task.cancel()
            try:
                await cancel_task
            except asyncio.CancelledError:
                pass


# ---------------------------------------------------------------------------
# Proto → Dict converters
# ---------------------------------------------------------------------------

def _messages_from_proto(msgs) -> list[dict]:
    """Convert proto Message list to dict list for engine consumption."""
    result = []
    for m in msgs:
        msg = {"role": m.role, "content": m.content}
        if m.tool_calls:
            msg["tool_calls"] = [
                {"id": tc.id, "name": tc.name, "arguments": tc.arguments}
                for tc in m.tool_calls
            ]
        if m.tool_call_id:
            msg["role"] = "tool"
            msg["tool_call_id"] = m.tool_call_id
            msg["content"] = m.tool_result
            if m.is_error:
                msg["is_error"] = True
        result.append(msg)
    return result


def _tools_from_proto(tools) -> list[dict]:
    """Convert proto ToolDefinition list to OpenAI-style tool dicts."""
    result = []
    for t in tools:
        result.append({
            "type": "function",
            "function": {
                "name": t.name,
                "description": t.description,
                "parameters": json.loads(t.parameters) if t.parameters else {},
            },
        })
    return result


def _build_response(event: dict) -> inference_pb2.GenerateResponse:
    """Convert engine event dict to proto GenerateResponse."""
    event_type = event["type"]

    if event_type == "token":
        return inference_pb2.GenerateResponse(
            event_type=inference_pb2.EVENT_TOKEN,
            token=event["token"],
        )
    elif event_type == "tool_use":
        return inference_pb2.GenerateResponse(
            event_type=inference_pb2.EVENT_TOOL_USE,
            tool_use=inference_pb2.ToolUse(
                id=event.get("id", ""),
                name=event.get("name", ""),
                arguments=event.get("arguments", ""),
            ),
        )
    elif event_type == "final":
        usage = None
        if "usage" in event:
            u = event["usage"]
            usage = inference_pb2.Usage(
                prompt_tokens=u.get("prompt_tokens", 0),
                completion_tokens=u.get("completion_tokens", 0),
                total_tokens=u.get("total_tokens", 0),
            )

        stop_map = {
            "STOP_END_TURN": inference_pb2.STOP_END_TURN,
            "STOP_MAX_TOKENS": inference_pb2.STOP_MAX_TOKENS,
            "STOP_TOOL_USE": inference_pb2.STOP_TOOL_USE,
            "STOP_CANCELLED": inference_pb2.STOP_CANCELLED,
            "STOP_ERROR": inference_pb2.STOP_ERROR,
        }

        return inference_pb2.GenerateResponse(
            event_type=inference_pb2.EVENT_FINAL,
            stop_reason=stop_map.get(event.get("stop_reason", ""), inference_pb2.STOP_UNSPECIFIED),
            finish_reason=event.get("finish_reason", ""),
            usage=usage,
        )

    return inference_pb2.GenerateResponse()


# ---------------------------------------------------------------------------
# Batch Inference Service
# ---------------------------------------------------------------------------

class BatchInferenceServicer(inference_pb2_grpc.BatchInferenceServiceServicer):
    """Implements the BatchInferenceService gRPC service."""

    def __init__(self, backend):
        self.backend = backend

    async def BatchGenerate(self, request, context: grpc.aio.ServicerContext):
        """Handle BatchGenerate RPC — stream results per request_id."""
        batch_id = request.batch_id
        items = list(request.requests)
        print(f"[batch_server] BatchGenerate: batch_id={batch_id}, requests={len(items)}")

        # Convert proto → dicts
        batch_requests = []
        for item in items:
            req_dict = {
                "request_id": item.request_id,
                "session_id": item.session_id,
                "messages": _messages_from_proto(item.messages),
                "system_prompt": item.system_prompt if item.system_prompt else "",
                "sampling_params": {
                    "max_tokens": item.sampling_params.max_tokens or 1024,
                    "temperature": item.sampling_params.temperature or 0.7,
                    "top_p": item.sampling_params.top_p or 0.9,
                    "top_k": item.sampling_params.top_k or 50,
                },
                "tools": _tools_from_proto(item.tools) if item.tools else None,
            }
            batch_requests.append(req_dict)

        try:
            async for req_id, event in self.backend.generate_batch(batch_requests):
                if context.cancelled():
                    print(f"[batch_server] Batch {batch_id} cancelled")
                    break

                response = _build_batch_response(req_id, event)
                await context.write(response)

        except Exception as e:
            print(f"[batch_server] Error in BatchGenerate: {e}")
            import traceback
            traceback.print_exc()
            # Gửi error cho từng request trong batch
            for item in items:
                err_resp = inference_pb2.BatchGenerateResponse(
                    request_id=item.request_id,
                    event_type=inference_pb2.EVENT_FINAL,
                    stop_reason=inference_pb2.STOP_ERROR,
                    finish_reason="error",
                    token=str(e),
                )
                await context.write(err_resp)


def _build_batch_response(request_id: str, event: dict) -> inference_pb2.BatchGenerateResponse:
    """Convert engine event → BatchGenerateResponse proto."""
    event_type = event["type"]

    if event_type == "token":
        return inference_pb2.BatchGenerateResponse(
            request_id=request_id,
            event_type=inference_pb2.EVENT_TOKEN,
            token=event["token"],
        )
    elif event_type == "tool_use":
        return inference_pb2.BatchGenerateResponse(
            request_id=request_id,
            event_type=inference_pb2.EVENT_TOOL_USE,
            tool_use=inference_pb2.ToolUse(
                id=event.get("id", ""),
                name=event.get("name", ""),
                arguments=event.get("arguments", ""),
            ),
        )
    elif event_type == "final":
        usage = None
        if "usage" in event:
            u = event["usage"]
            usage = inference_pb2.Usage(
                prompt_tokens=u.get("prompt_tokens", 0),
                completion_tokens=u.get("completion_tokens", 0),
                total_tokens=u.get("total_tokens", 0),
            )

        stop_map = {
            "STOP_END_TURN": inference_pb2.STOP_END_TURN,
            "STOP_MAX_TOKENS": inference_pb2.STOP_MAX_TOKENS,
            "STOP_TOOL_USE": inference_pb2.STOP_TOOL_USE,
            "STOP_CANCELLED": inference_pb2.STOP_CANCELLED,
            "STOP_ERROR": inference_pb2.STOP_ERROR,
        }

        return inference_pb2.BatchGenerateResponse(
            request_id=request_id,
            event_type=inference_pb2.EVENT_FINAL,
            stop_reason=stop_map.get(event.get("stop_reason", ""), inference_pb2.STOP_UNSPECIFIED),
            finish_reason=event.get("finish_reason", ""),
            usage=usage,
        )

    return inference_pb2.BatchGenerateResponse(request_id=request_id)


# ---------------------------------------------------------------------------
# Server bootstrap
# ---------------------------------------------------------------------------

async def serve(port: int = DEFAULT_PORT, model_id: str | None = None,
                engine_name: str = "transformers", gguf: str | None = None,
                llama_port: int = 8081, llama_bin: str = "llama-server"):
    """Start the gRPC inference server."""
    print("[server] Starting AI Factory Inference Worker...")
    print(f"[server] gRPC port: {port}")

    # Load model via backend registry (transformers | llama)
    from .engines import get_backend
    backend = get_backend(engine_name, model_id=model_id, gguf=gguf,
                          llama_port=llama_port, llama_bin=llama_bin)
    backend.load()

    # Create gRPC server
    server = grpc.aio.server(
        futures.ThreadPoolExecutor(max_workers=4),
        options=[
            ("grpc.max_send_message_length", 100 * 1024 * 1024),   # 100MB
            ("grpc.max_receive_message_length", 10 * 1024 * 1024),  # 10MB
            ("grpc.keepalive_time_ms", 30000),
            ("grpc.keepalive_timeout_ms", 10000),
        ],
    )

    servicer = InferenceServicer(backend)
    inference_pb2_grpc.add_InferenceServiceServicer_to_server(servicer, server)

    # Register batch inference service
    batch_servicer = BatchInferenceServicer(backend)
    inference_pb2_grpc.add_BatchInferenceServiceServicer_to_server(batch_servicer, server)

    server.add_insecure_port(f"[::]:{port}")

    await server.start()
    print(f"[server] Worker ready on port {port}. Waiting for Go server...")

    # Graceful shutdown on SIGINT/SIGTERM
    stop_event = asyncio.Event()

    def _signal_handler(sig, frame):
        print(f"\n[server] Received signal {sig}, shutting down...")
        stop_event.set()

    signal.signal(signal.SIGINT, _signal_handler)
    signal.signal(signal.SIGTERM, _signal_handler)

    await stop_event.wait()
    print("[server] Stopping...")
    await server.stop(5)
    backend.unload()
    print("[server] Worker shut down.")


def main():
    """Entry point for python -m worker.server"""
    import argparse
    parser = argparse.ArgumentParser(description="AI Factory Inference Worker")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help="gRPC port")
    parser.add_argument("--model", type=str, default=None, help="Model ID (default: Llama 3.2 3B)")
    parser.add_argument("--engine", type=str, default="transformers",
                        help="transformers | llama")
    parser.add_argument("--gguf", type=str, default=None,
                        help="Path hoặc repo GGUF (chỉ khi --engine llama)")
    parser.add_argument("--llama-port", type=int, default=8081)
    parser.add_argument("--llama-bin", type=str, default="llama-server")
    args = parser.parse_args()

    asyncio.run(serve(port=args.port, model_id=args.model, engine_name=args.engine,
                      gguf=args.gguf, llama_port=args.llama_port, llama_bin=args.llama_bin))


if __name__ == "__main__":
    main()
