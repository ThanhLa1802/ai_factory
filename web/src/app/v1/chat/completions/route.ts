import type { NextRequest } from "next/server";

// Proxy the SSE inference endpoint to the Go server via fetch() so the stream
// is passed through token-by-token. next.config `rewrites()` buffers streaming
// responses, which made the answer appear all at once instead of streaming.
const apiUrl = process.env.AI_FACTORY_API_URL || "http://localhost:8080";

export async function POST(request: NextRequest) {
  const upstream = await fetch(`${apiUrl}/v1/chat/completions`, {
    method: "POST",
    headers: {
      "Content-Type": request.headers.get("content-type") || "application/json",
      "x-session-id": request.headers.get("x-session-id") || "",
      Authorization: request.headers.get("authorization") || "",
    },
    body: await request.arrayBuffer(),
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
