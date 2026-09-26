# Slim inference worker — gRPC server + OpenAI-compatible proxy engines.
#
# Build context = python-worker/:
#   docker build -f python-worker/Dockerfile -t ai-factory/worker:dev python-worker
#
# NOTE: this image intentionally does NOT install torch/transformers/
# bitsandbytes, so it supports the `llama` and `vllm` engines (both proxy an
# OpenAI-compatible HTTP server) but NOT `--engine transformers`. For that, add
# the pyproject dependencies (torch is multi-GB) to a derived image.
FROM python:3.12-slim

WORKDIR /app

RUN pip install --no-cache-dir "grpcio>=1.78.0" "protobuf>=6.31.1" "httpx>=0.27"

# pb/ stubs are committed; regenerate with `python -m worker.generate_proto`
# after editing proto/inference.proto.
COPY . /app/

ENV PYTHONUNBUFFERED=1
EXPOSE 50051

ENTRYPOINT ["python", "-m", "worker.server"]
