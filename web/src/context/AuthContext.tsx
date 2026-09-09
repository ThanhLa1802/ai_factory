"use client";

import { createContext, useCallback, useContext, useMemo, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { apiFetch } from "@/lib/api";
import { clearToken, decodeToken, getToken, setToken as storeToken } from "@/lib/auth";
import type { Claims } from "@/lib/types";

interface AuthState {
  token: string | null;
  claims: Claims | null;
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthState | undefined>(undefined);

// Auth state lives in localStorage (an external store), so we sync it into React
// with useSyncExternalStore instead of useState + effect. This is the sanctioned
// pattern for localStorage-backed state and avoids the hydration mismatch that
// plain lazy useState would cause under SSR.
const listeners = new Set<() => void>();

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function emit() {
  for (const l of listeners) l();
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const token = useSyncExternalStore(
    subscribe,
    () => getToken(), // client snapshot
    () => null, // server snapshot (no localStorage during SSR)
  );

  const claims = useMemo(() => (token ? decodeToken(token) : null), [token]);

  const login = useCallback(async (username: string, password: string) => {
    const data = await apiFetch<{ access_token: string }>("/api/v1/auth/login", {
      method: "POST",
      body: { username, password },
    });
    storeToken(data.access_token);
    emit();
  }, []);

  const logout = useCallback(() => {
    clearToken();
    emit();
  }, []);

  const value = useMemo(
    () => ({ token, claims, login, logout }),
    [token, claims, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
