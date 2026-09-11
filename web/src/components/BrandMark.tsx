// Brand mark — an 8-point "spark" on an indigo→violet square. Shared by the
// sidebar header, the assistant avatar in chat, and the login screen.
export default function BrandMark({ size = 28 }: { size?: number }) {
  return (
    <div
      className="flex shrink-0 items-center justify-center rounded-lg"
      style={{
        width: size,
        height: size,
        background: "linear-gradient(135deg, #6366f1, #8b5cf6)",
      }}
      aria-hidden="true"
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="white"
        strokeWidth="2.2"
        strokeLinecap="round"
        style={{ width: size * 0.55, height: size * 0.55 }}
      >
        <path d="M12 3v18M3 12h18M5.6 5.6l12.8 12.8M18.4 5.6L5.6 18.4" />
      </svg>
    </div>
  );
}
