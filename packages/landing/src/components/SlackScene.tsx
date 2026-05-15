import { useEffect, useRef, useState } from "react";

/**
 * Scroll-driven Slack mockup — neo-brutalism edition.
 */

const INCOMING = "@here just pulled main and the app won't start — missing STRIPE_SECRET_KEY and REDIS_URL. Can someone send me the latest .env?";
const DRAFT_1 = "Check #dev-onboarding, I think Sarah posted a 1Password link last week but it might be exp";
const DRAFT_2 = "Pull the latest .env with propagate — everyone gets updated vars automatically and securely.";
const TYPO_DRAFT = "hang on let me get send you the";
const TYPO_BACKTO = 0;

function buildKeystrokeWeights(text: string): number[] {
  const w: number[] = [];
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    let base = 1;
    if (c === " ") base = 1.6;
    else if (",;:".includes(c)) base = 2.4;
    else if (".!?".includes(c)) base = 3.2;
    const jitter = 0.7 + ((i * 9301 + 49297) % 233) / 233 * 0.8;
    w.push(base * jitter);
  }
  return w;
}

function charsAtProgress(text: string, t: number): number {
  if (t <= 0) return 0;
  if (t >= 1) return text.length;
  const weights = buildKeystrokeWeights(text);
  const total = weights.reduce((a, b) => a + b, 0);
  const target = t * total;
  let acc = 0;
  for (let i = 0; i < weights.length; i++) {
    acc += weights[i];
    if (acc >= target) return i + 1;
  }
  return text.length;
}

function computeState(p: number) {
  const incomingVisible = p >= 0.06;
  let draft = "";

  if (p >= 0.1 && p < 0.32) {
    const t = (p - 0.1) / (0.32 - 0.1);
    draft = DRAFT_1.slice(0, charsAtProgress(DRAFT_1, t));
  } else if (p >= 0.32 && p < 0.4) {
    draft = DRAFT_1;
  } else if (p >= 0.4 && p < 0.55) {
    const t = (p - 0.4) / (0.55 - 0.4);
    const remaining = Math.round(DRAFT_1.length * (1 - t));
    draft = DRAFT_1.slice(0, Math.max(0, remaining));
  } else if (p >= 0.55 && p < 0.6) {
    draft = "";
  } else if (p >= 0.6 && p < 0.7) {
    const t = (p - 0.6) / (0.7 - 0.6);
    draft = TYPO_DRAFT.slice(0, charsAtProgress(TYPO_DRAFT, t));
  } else if (p >= 0.7 && p < 0.74) {
    draft = TYPO_DRAFT;
  } else if (p >= 0.74 && p < 0.77) {
    const t = (p - 0.74) / (0.77 - 0.74);
    const len = Math.round(TYPO_DRAFT.length - (TYPO_DRAFT.length - TYPO_BACKTO) * t);
    draft = TYPO_DRAFT.slice(0, Math.max(TYPO_BACKTO, len));
  } else if (p >= 0.77 && p <= 1) {
    const t = Math.min(1, (p - 0.77) / (0.95 - 0.77));
    const remaining = DRAFT_2.slice(TYPO_BACKTO);
    const revealed = remaining.slice(0, charsAtProgress(remaining, t));
    draft = DRAFT_2.slice(0, TYPO_BACKTO) + revealed;
  }

  const isTyping =
    (p >= 0.1 && p < 0.32) ||
    (p >= 0.4 && p < 0.55) ||
    (p >= 0.6 && p < 0.7) ||
    (p >= 0.74 && p < 0.77) ||
    (p >= 0.77 && p < 0.95);

  return { incomingVisible, draft, isTyping };
}

export function SlackScene() {
  const wrapperRef     = useRef<HTMLDivElement>(null);
  const barFillRef     = useRef<HTMLDivElement>(null);
  const barContainerRef = useRef<HTMLDivElement>(null);
  const [progress, setProgress] = useState(0);

  useEffect(() => {
    let raf = 0;
    const onScroll = () => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => {
        const el = wrapperRef.current;
        if (!el) return;
        const rect  = el.getBoundingClientRect();
        const total = el.offsetHeight - window.innerHeight;
        const scrolled = Math.min(Math.max(-rect.top, 0), total);
        const p = total > 0 ? scrolled / total : 0;

        // Direct DOM — no React re-render
        if (barFillRef.current) barFillRef.current.style.width = `${p * 100}%`;
        if (barContainerRef.current) {
          barContainerRef.current.style.opacity = (p > 0.001 && p < 0.999) ? "1" : "0";
        }
        setProgress(p);
      });
    };
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
    };
  }, []);

  const { incomingVisible, draft, isTyping } = computeState(progress);
  const showCaret = draft.length > 0;

  return (
    <div ref={wrapperRef} className="relative" style={{ height: "360vh" }}>
      {/* Progress bar — fixed so it never jumps on Mac rubber-band scroll */}
      <div
        ref={barContainerRef}
        style={{
          position:   "fixed",
          top:        "3.5rem",   /* flush below the fixed nav */
          left:       0,
          right:      0,
          zIndex:     48,
          height:     "6px",
          background: "#1a1a1a",
          opacity:    0,
          transition: "opacity 0.25s",
          pointerEvents: "none",
        }}
      >
        <div
          ref={barFillRef}
          style={{
            height:     "100%",
            width:      "0%",
            background: "#FFE500",
            willChange: "width",
          }}
        />
      </div>

      <div
        className="sticky top-14 flex items-center justify-center overflow-hidden px-4"
        style={{ height: "calc(100vh - 3.5rem)" }}
      >
        <div aria-hidden className="mesh-bg" style={{ position: "absolute", inset: 0, zIndex: 0 }} />

        <div
          className="grid w-full items-center gap-10"
          style={{
            position: "relative",
            zIndex: 1,
            maxWidth: "72rem",
            gridTemplateColumns: "1fr",
          }}
        >
          <style>{`@media(min-width:1024px){.slack-grid{grid-template-columns:0.7fr 1.3fr!important}}`}</style>
          <div className="slack-grid grid w-full items-center gap-10" style={{ gridTemplateColumns: "1fr" }}>
            {/* Left: headline */}
            <div>
              <div
                className="inline-block mb-6 px-3 py-1 text-xs font-extrabold uppercase tracking-widest"
                style={{ border: "2px solid #fff", color: "#888", letterSpacing: "0.18em" }}
              >
                Before Propagate
              </div>
              <h2
                className="font-extrabold leading-none"
                style={{ fontSize: "clamp(2rem,4.5vw,3.2rem)", letterSpacing: "-0.03em", color: "#fff" }}
              >
                .env over Slack&nbsp;<br />
                <span style={{ color: "#FFE500" }}>Never </span><span style={{ color: "#FFE500", fontFamily: "'Instrument Serif','Iowan Old Style',Georgia,serif", fontStyle: "italic", fontWeight: 400, fontSize: "1.05em", background: "rgba(255,229,0,0.2)", padding: "0 0.15em" }}>again</span>
              </h2>
              <p className="mt-5 max-w-sm text-sm leading-relaxed" style={{ color: "#888" }}>
                Every time someone adds a new secret, your team scrambles — DMs, expired 1Password links, pinned messages from months ago. Propagate makes sharing secrets a single CLI command.
              </p>
            </div>

            {/* Right: Slack mockup */}
            <SlackMock
              incomingVisible={incomingVisible}
              draft={draft}
              caret={showCaret}
              isTyping={isTyping}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

function SlackMock({
  incomingVisible,
  draft,
  caret,
  isTyping,
}: {
  incomingVisible: boolean;
  draft: string;
  caret: boolean;
  isTyping: boolean;
}) {
  return (
    <div style={{ border: "2px solid #fff", boxShadow: "6px 6px 0 #FFE500" }}>
      <div style={{ background: "#111" }}>
        {/* Window chrome */}
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: "8px",
            padding: "12px 20px",
            borderBottom: "2px solid #fff",
            background: "#1a1a1a",
          }}
        >
          <span style={{ height: 12, width: 12, borderRadius: "50%", background: "#ff5f57", display: "inline-block" }} />
          <span style={{ height: 12, width: 12, borderRadius: "50%", background: "#ffbd2e", display: "inline-block" }} />
          <span style={{ height: 12, width: 12, borderRadius: "50%", background: "#28c840", display: "inline-block" }} />
          <div style={{ marginLeft: 16, display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ color: "#FFE500", fontWeight: 800, fontSize: 16 }}>#</span>
            <span style={{ fontSize: 15, fontWeight: 700, color: "#fff" }}>dev</span>
            <span style={{ fontSize: 12, color: "#666" }}>· 8 members</span>
          </div>
        </div>

        {/* Message area */}
        <div style={{ minHeight: 280, padding: "24px 32px", display: "flex", flexDirection: "column", gap: 20 }}>
          <div
            style={{
              display: "flex",
              gap: 14,
              opacity: incomingVisible ? 1 : 0,
              transform: incomingVisible ? "translateY(0)" : "translateY(12px)",
              transition: "opacity 0.4s, transform 0.4s",
            }}
          >
            <div
              style={{
                height: 40,
                width: 40,
                flexShrink: 0,
                background: "#FFE500",
                border: "2px solid #fff",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                fontWeight: 800,
                fontSize: 14,
                color: "#000",
              }}
            >
              MC
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: "flex", alignItems: "baseline", gap: 8 }}>
                <span style={{ fontSize: 15, fontWeight: 800, color: "#fff" }}>Maya Chen</span>
                <span style={{ fontSize: 12, color: "#555" }}>10:42 AM</span>
              </div>
              <div
                style={{
                  marginTop: 6,
                  display: "inline-block",
                  maxWidth: "100%",
                  padding: "12px 16px",
                  background: "#1a1a1a",
                  border: "2px solid #333",
                  boxShadow: "3px 3px 0 #333",
                }}
              >
                <p style={{ fontSize: 15, lineHeight: 1.6, color: "#eee" }}>{INCOMING}</p>
              </div>
            </div>
          </div>

          {isTyping && (
            <div style={{ display: "flex", alignItems: "center", gap: 8, paddingLeft: 54, fontSize: 13, color: "#666" }}>
              <span style={{ display: "flex", gap: 4 }}>
                {[0, 150, 300].map((delay) => (
                  <span
                    key={delay}
                    style={{
                      height: 6,
                      width: 6,
                      borderRadius: "50%",
                      background: "#FFE500",
                      display: "inline-block",
                      animation: `bounce 0.8s ${delay}ms infinite`,
                    }}
                  />
                ))}
              </span>
              <span>you are typing…</span>
              <style>{`
                @keyframes bounce {
                  0%, 100% { transform: translateY(0); }
                  50%       { transform: translateY(-4px); }
                }
              `}</style>
            </div>
          )}
        </div>

        {/* Composer */}
        <div style={{ padding: "0 24px 24px" }}>
          <div
            style={{
              background: "#0c0c0c",
              border: "2px solid #fff",
              padding: "14px 18px",
            }}
          >
            <p style={{ minHeight: "1.5rem", fontSize: 15, lineHeight: 1.6, color: "#fff", fontFamily: "inherit" }}>
              {draft || <span style={{ color: "#444" }}>Message #dev</span>}
              {caret && (
                <span
                  style={{
                    marginLeft: 1,
                    display: "inline-block",
                    width: 2,
                    height: "0.9em",
                    verticalAlign: "-0.05em",
                    background: "#FFE500",
                    animation: "blink 1s ease-in-out infinite",
                  }}
                />
              )}
            </p>
            <div style={{ marginTop: 10, display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <div style={{ display: "flex", gap: 6 }}>
                {["B", "I", "U", "{}"].map((s) => (
                  <span
                    key={s}
                    style={{
                      display: "grid",
                      placeItems: "center",
                      height: 28,
                      width: 28,
                      fontSize: 11,
                      fontWeight: 700,
                      color: "#555",
                      background: "#1a1a1a",
                      border: "2px solid #333",
                      cursor: "default",
                    }}
                  >
                    {s}
                  </span>
                ))}
              </div>
              <button
                style={{
                  background: "#FFE500",
                  color: "#000",
                  border: "2px solid #fff",
                  boxShadow: "3px 3px 0 #fff",
                  padding: "6px 14px",
                  fontSize: 12,
                  fontWeight: 800,
                  cursor: "pointer",
                  fontFamily: "inherit",
                }}
              >
                Send →
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
