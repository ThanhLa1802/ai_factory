import type { Claims, Role } from "./types";

const TOKEN_KEY = "aif_token";

// The token lives in localStorage (an external store), so React reads it through
// useSyncExternalStore. The listener set + emit live here (not in AuthContext) so
// non-React code — notably apiFetch's 401 handling — can clear the token and
// notify the UI without importing React.
const listeners = new Set<() => void>();

export function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function emit() {
  for (const l of listeners) l();
}

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string) {
  window.localStorage.setItem(TOKEN_KEY, token);
  emit();
}

export function clearToken() {
  window.localStorage.removeItem(TOKEN_KEY);
  emit();
}

// decodeToken parses the JWT payload (base64) without verifying the signature —
// the server verifies it on every API call. This is only used to gate the UI.
export function decodeToken(token: string): Claims | null {
  try {
    const part = token.split(".")[1];
    const b64 = part.replace(/-/g, "+").replace(/_/g, "/");
    return JSON.parse(atob(b64)) as Claims;
  } catch {
    return null;
  }
}

export const ROLE_LABEL: Record<Role, string> = {
  PLATFORM_ADMIN: "Platform Admin",
  TENANT_ADMIN: "Tenant Admin",
  TENANT_DEVELOPER: "Tenant Developer",
  TENANT_VIEWER: "Tenant Viewer",
};

export function isPlatformAdmin(role?: Role): boolean {
  return role === "PLATFORM_ADMIN";
}
