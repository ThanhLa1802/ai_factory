"use client";

import { useEffect, useState } from "react";
import { useAuth } from "@/context/AuthContext";

// AuthImage loads a chat upload behind auth. <img src> cannot carry the bearer
// token, so the bytes are fetched as a blob and handed to React as an object
// URL (same approach as the generated-image gallery).
export default function AuthImage({
  id,
  alt,
  className,
}: {
  id: string;
  alt: string;
  className?: string;
}) {
  const { token } = useAuth();
  const [src, setSrc] = useState<string | null>(null);

  useEffect(() => {
    if (!id || !token) return;
    let cancelled = false;
    let url: string | null = null;
    fetch(`/api/v1/uploads/${id}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((resp) => (resp.ok ? resp.blob() : null))
      .then((blob) => {
        if (cancelled || !blob) return;
        url = URL.createObjectURL(blob);
        setSrc(url);
      })
      .catch(() => {
        // Missing upload: keep the placeholder instead of breaking the message.
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [id, token]);

  if (!src) {
    return <div className={`${className ?? ""} bg-[var(--surface2)]`} aria-hidden="true" />;
  }
  // eslint-disable-next-line @next/next/no-img-element -- object URL from an authed fetch
  return <img src={src} alt={alt} className={className} />;
}
