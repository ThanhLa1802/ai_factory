import type { NextConfig } from "next";

// Base URL of the Go API server. Override with AI_FACTORY_API_URL in
// web/.env.local when the backend runs on a non-default host/port.
const apiUrl = process.env.AI_FACTORY_API_URL || "http://localhost:8080";

// The Next.js app proxies the Go server's JSON API routes so the browser
// talks same-origin (no CORS handling in the UI). The SSE inference route
// (/v1/chat/completions) is NOT proxied here — rewrites() buffer streaming
// responses, so it's handled by a streaming Route Handler instead
// (src/app/v1/chat/completions/route.ts). The Go server still serves the old
// static ui/ on /chat, /keys — those paths are NOT proxied.
const nextConfig: NextConfig = {
  async rewrites() {
    return [
      { source: "/api/v1/:path*", destination: `${apiUrl}/api/v1/:path*` },
      { source: "/health", destination: `${apiUrl}/health` },
      { source: "/metrics", destination: `${apiUrl}/metrics` },
    ];
  },
};

export default nextConfig;
