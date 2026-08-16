import ChatClient from "@/components/ChatClient";

export const metadata = { title: "Chat · AI Factory" };

export default function ChatPage() {
  return (
    <div className="flex h-full flex-col bg-[var(--bg)]">
      <ChatClient />
    </div>
  );
}
