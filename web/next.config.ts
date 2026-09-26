import type { NextConfig } from "next";

// Base URL of the Go API server. Override with AI_FACTORY_API_URL in
// web/.env.local when the backend runs on a non-default host/port.
const apiUrl = process.env.AI_FACTORY_API_URL || "http://localhost:8080";

// The Next.js app proxies the Go server's JSON API routes so the browser
// talks same-origin (no CORS handling in the UI). The SSE inference route
// (/v1/chat/completions) is NOT proxied here — rewrites() buffer streaming
// responses, so it's handled by a streaming Route Handler instead
// (src/app/v1/chat/completions/route.ts). The old Go static ui/ still serves
// /chat and /keys on :8080, but the Next app owns those paths: /keys is gone
// (API keys moved into /platform), so redirect it to the API Keys tab.
const nextConfig: NextConfig = {
  // Emit a self-contained server bundle for a slim production image
  // (web/Dockerfile copies `.next/standalone`).
  output: "standalone",
  async redirects() {
    return [{ source: "/keys", destination: "/platform?tab=keys", permanent: false }];
  },
  async rewrites() {
    return [
      { source: "/api/v1/:path*", destination: `${apiUrl}/api/v1/:path*` },
      { source: "/health", destination: `${apiUrl}/health` },
      { source: "/metrics", destination: `${apiUrl}/metrics` },
    ];
  },
};

export default nextConfig;
