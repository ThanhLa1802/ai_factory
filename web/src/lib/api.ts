import { getToken } from "./auth";

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

interface FetchOptions {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
}

// apiFetch is the control-plane helper: attaches the JWT, JSON-encodes bodies,
// and throws ApiError with the server's {code,message} on non-2xx.
export async function apiFetch<T>(
  path: string,
  opts: FetchOptions = {},
): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = { ...(opts.headers || {}) };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  const resp = await fetch(path, {
    method: opts.method || "GET",
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });

  if (!resp.ok) {
    let code = "ERROR";
    let message = resp.statusText;
    try {
      const data = await resp.json();
      if (data?.error) {
        code = data.error.code || code;
        message = data.error.message || message;
      }
    } catch {
      // non-JSON error body
    }
    throw new ApiError(resp.status, code, message);
  }

  if (resp.status === 204) return undefined as T;
  return (await resp.json()) as T;
}
