"""
Generate Python gRPC stubs from the proto files.

Run once: python -m worker.generate_proto
Generates stubs for inference.proto (text) and image.proto (text-to-image).
"""

import os
import sys
from pathlib import Path


PROTO_FILES = ["inference.proto", "image.proto"]


def generate():
    proto_dir = Path(__file__).parent.parent.parent / "proto"
    output_dir = Path(__file__).parent / "pb"

    output_dir.mkdir(exist_ok=True)

    # Generate Python stubs
    import grpc_tools.protoc

    args = [
        sys.argv[0],
        f"-I{proto_dir}",
        f"--python_out={output_dir}",
        f"--grpc_python_out={output_dir}",
        *[str(proto_dir / name) for name in PROTO_FILES],
    ]

    # grpc_tools.protoc.main wants sys.argv-style args
    result = grpc_tools.protoc.main(args)
    if result != 0:
        print(f"[proto] Failed with code {result}", file=sys.stderr)
        sys.exit(result)

    print(f"[proto] Generated stubs in {output_dir}")

    # Fix absolute import → relative import in each *_pb2_grpc.py so the
    # generated modules import cleanly as `worker.pb.<name>_pb2`.
    for name in PROTO_FILES:
        stem = Path(name).stem
        grpc_file = output_dir / f"{stem}_pb2_grpc.py"
        if grpc_file.exists():
            content = grpc_file.read_text(encoding="utf-8")
            content = content.replace(
                f"import {stem}_pb2 as {stem}__pb2",
                f"from worker.pb import {stem}_pb2 as {stem}__pb2",
            )
            grpc_file.write_text(content, encoding="utf-8")
            print(f"[proto] Fixed import in {grpc_file}")

    # Create __init__.py in pb directory
    (output_dir / "__init__.py").touch()


if __name__ == "__main__":
    generate()
