import LoginForm from "@/components/LoginForm";

export const metadata = { title: "Sign in · AI Factory" };

export default function LoginPage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--bg)] px-4">
      <LoginForm />
    </div>
  );
}
