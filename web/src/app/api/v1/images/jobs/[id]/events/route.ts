import type { NextRequest } from "next/server";

// Proxy the image job SSE endpoint to the Go server via fetch() so progress
// events stream through. next.config `rewrites()` buffer streaming responses,
// so this explicit Route Handler (which wins over the /api/v1 rewrite) is used
// instead — mirroring /v1/chat/completions.
const apiUrl = process.env.AI_FACTORY_API_URL || "http://localhost:8080";

export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  const upstream = await fetch(`${apiUrl}/api/v1/images/jobs/${id}/events`, {
    headers: {
      Accept: "text/event-stream",
      Authorization: request.headers.get("authorization") || "",
    },
  });

  return new Response(upstream.body, {
    status: upstream.status,
    headers: {
      "Content-Type":
        upstream.headers.get("content-type") || "text/event-stream",
      "Cache-Control": "no-cache",
      "X-Accel-Buffering": "no",
    },
  });
}
