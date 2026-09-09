"""
Generate Python gRPC stubs from inference.proto.

Run once: python -m worker.generate_proto
"""

import os
import sys
from pathlib import Path


def generate():
    proto_dir = Path(__file__).parent.parent.parent / "proto"
    proto_file = proto_dir / "inference.proto"
    output_dir = Path(__file__).parent / "pb"

    output_dir.mkdir(exist_ok=True)

    # Generate Python stubs
    import grpc_tools.protoc

    args = [
        sys.argv[0],
        f"-I{proto_dir}",
        f"--python_out={output_dir}",
        f"--grpc_python_out={output_dir}",
        str(proto_file),
    ]

    # grpc_tools.protoc.main wants sys.argv-style args
    result = grpc_tools.protoc.main(args)
    if result == 0:
        print(f"[proto] Generated stubs in {output_dir}")
        # Fix absolute import → relative import in _grpc.py
        grpc_file = output_dir / "inference_pb2_grpc.py"
        if grpc_file.exists():
            content = grpc_file.read_text(encoding="utf-8")
            content = content.replace(
                "import inference_pb2 as inference__pb2",
                "from worker.pb import inference_pb2 as inference__pb2",
            )
            grpc_file.write_text(content, encoding="utf-8")
            print(f"[proto] Fixed import in {grpc_file}")
        # Create __init__.py in pb directory
        init_file = output_dir / "__init__.py"
        init_file.touch()
    else:
        print(f"[proto] Failed with code {result}", file=sys.stderr)
        sys.exit(result)


if __name__ == "__main__":
    generate()
