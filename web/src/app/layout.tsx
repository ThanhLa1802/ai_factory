import type { Metadata } from "next";
import "./globals.css";
import { AuthProvider } from "@/context/AuthContext";

export const metadata: Metadata = {
  title: "AI Factory",
  description: "Inference platform",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="vi" className="h-full antialiased">
      <body className="min-h-full bg-[var(--bg)] text-[var(--text)]">
        <AuthProvider>{children}</AuthProvider>
      </body>
    </html>
  );
}
