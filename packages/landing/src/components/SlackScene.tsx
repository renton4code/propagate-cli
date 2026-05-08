import { useEffect, useRef, useState } from "react";

/**
 * Scroll-driven Slack mockup.
 * - A tall outer wrapper provides scroll distance.
 * - A sticky inner viewport holds the mockup.
 * - Scroll progress (0 → 1) drives a scripted typing timeline.
 */

const INCOMING = "@here, I see we moved to clickhouse, can smb share the staging creds? 🙏";
const DRAFT_1 = "Dude again, I sent 1Password link in #backend channel 15 minutes ago...";
const DRAFT_2 = "run `propagate env pull --scope staging` — I just added you to propagate.yaml";
const TYPO_DRAFT = "run `propagte";
const TYPO_BACKTO = 7;

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
  const wrapperRef = useRef<HTMLDivElement>(null);
  const [progress, setProgress] = useState(0);

  useEffect(() => {
    const onScroll = () => {
      const el = wrapperRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const vh = window.innerHeight;
      const total = el.offsetHeight - vh;
      const scrolled = Math.min(Math.max(-rect.top, 0), total);
      setProgress(total > 0 ? scrolled / total : 0);
    };
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
    };
  }, []);

  const { incomingVisible, draft, isTyping } = computeState(progress);
  const showCaret = progress > 0.08 && progress < 0.97;

  return (
    <div ref={wrapperRef} className="relative" style={{ height: "360vh" }}>
      {/* Progress bar — sits flush under the fixed nav (h-14 = 3.5rem) */}
      <div className="sticky top-14 z-40 h-[2px] w-full" style={{ background: "var(--neu-dark)" }}>
        <div
          className="h-full"
          style={{
            width: `${progress * 100}%`,
            background: "var(--primary)",
            boxShadow: "0 0 16px var(--primary), 0 0 4px var(--primary)",
            transition: "width 0.05s linear",
          }}
        />
      </div>

      <div className="sticky top-14 flex h-[calc(100vh-3.5rem)] items-center justify-center overflow-hidden px-4">
        <div aria-hidden={true} className="mesh-bg -z-10" />

        <div className="grid w-full max-w-6xl grid-cols-1 items-center gap-12 lg:grid-cols-[1fr_1.2fr]">
          {/* Left: headline */}
          <div>
            <div className="mb-6 inline-flex items-center gap-2">
              <span
                className="h-1.5 w-1.5 rounded-full"
                style={{ background: "var(--stripe-2)", boxShadow: "0 0 8px var(--stripe-2)" }}
              />
              <span className="eyebrow">Before Propagate</span>
            </div>
            <h2 className="text-4xl font-bold leading-tight text-zinc-50 md:text-6xl">
              .env over Slack.<br />
              <span className="text-gradient">Never again.</span>
            </h2>
            <p className="mt-6 max-w-md text-base leading-relaxed text-zinc-400">
              We add new variables, people ask for it.
              Passowrd manager links everywhere, but it's still a pain to manage.
              Declare variables once and sync them securely across team, CI and coding agents.
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
    <div
      className="gradient-border p-[1px]"
      style={{ boxShadow: "0 30px 80px -20px rgba(0,0,0,0.6), var(--shadow-neu)" }}
    >
      <div className="overflow-hidden rounded-[19px]" style={{ background: "var(--surface)" }}>
        {/* Window chrome */}
        <div className="flex items-center gap-2 px-5 py-4" style={{ borderBottom: "1px solid var(--border)" }}>
          <span className="h-3 w-3 rounded-full" style={{ background: "oklch(0.65 0.2 25)" }} />
          <span className="h-3 w-3 rounded-full" style={{ background: "oklch(0.78 0.16 80)" }} />
          <span className="h-3 w-3 rounded-full" style={{ background: "oklch(0.72 0.18 150)" }} />
          <div className="ml-4 flex items-center gap-2">
            <span style={{ color: "var(--primary)" }}>#</span>
            <span className="text-sm font-semibold" style={{ color: "var(--foreground)" }}>
              dev
            </span>
            <span className="text-xs" style={{ color: "var(--muted-foreground)" }}>
              · 8 members
            </span>
          </div>
        </div>

        {/* Message area */}
        <div className="min-h-[300px] space-y-4 px-6 pb-6 pt-4">
          <div
            className="flex gap-3 transition-all duration-500"
            style={{
              opacity: incomingVisible ? 1 : 0,
              transform: incomingVisible ? "translateY(0)" : "translateY(12px)",
            }}
          >
            <div
              className="h-10 w-10 flex-shrink-0 rounded-xl"
              style={{
                background: "var(--gradient-primary)",
                boxShadow: "inset 2px 2px 4px rgba(255,255,255,0.15), inset -2px -2px 4px rgba(0,0,0,0.3)",
              }}
            />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2">
                <span className="text-sm font-bold" style={{ color: "var(--foreground)" }}>
                  Maya Chen
                </span>
                <span className="text-[11px]" style={{ color: "var(--muted-foreground)" }}>
                  10:42 AM
                </span>
              </div>
              <div
                className="mt-1 inline-block max-w-full rounded-2xl rounded-tl-sm px-4 py-2.5"
                style={{ background: "var(--surface-2)", boxShadow: "var(--shadow-neu-sm)" }}
              >
                <p className="text-sm leading-relaxed" style={{ color: "var(--foreground)" }}>
                  {INCOMING}
                </p>
              </div>
            </div>
          </div>

          {isTyping && (
            <div
              className="flex items-center gap-2 pl-13 text-xs"
              style={{ color: "var(--muted-foreground)" }}
            >
              <span className="flex gap-1">
                <span
                  className="h-1.5 w-1.5 animate-bounce rounded-full"
                  style={{ background: "var(--primary)", animationDelay: "0ms" }}
                />
                <span
                  className="h-1.5 w-1.5 animate-bounce rounded-full"
                  style={{ background: "var(--primary)", animationDelay: "150ms" }}
                />
                <span
                  className="h-1.5 w-1.5 animate-bounce rounded-full"
                  style={{ background: "var(--primary)", animationDelay: "300ms" }}
                />
              </span>
              <span>you are typing…</span>
            </div>
          )}
        </div>

        {/* Composer */}
        <div className="px-5 pb-5">
          <div className="neu-inset px-4 py-3">
            <p className="min-h-[1.5rem] text-sm leading-relaxed" style={{ color: "var(--foreground)" }}>
              {draft || (
                <span style={{ color: "var(--muted-foreground)" }}>
                  Message #dev
                </span>
              )}
              {caret && (
                <span
                  className="ml-[1px] inline-block h-4 w-[2px] -mb-0.5 align-middle"
                  style={{
                    background: "var(--primary)",
                    animation: "blink 1s steps(2) infinite",
                    boxShadow: "0 0 6px var(--primary)",
                  }}
                />
              )}
            </p>
            <div className="mt-3 flex items-center justify-between">
              <div className="flex gap-2">
                {["B", "I", "U", "{ }"].map((s) => (
                  <span
                    key={s}
                    className="grid h-7 w-7 place-items-center rounded-md text-[11px]"
                    style={{
                      color: "var(--muted-foreground)",
                      boxShadow: "var(--shadow-neu-sm)",
                      background: "var(--surface)",
                    }}
                  >
                    {s}
                  </span>
                ))}
              </div>
              <button
                className="rounded-xl px-4 py-2 text-xs font-bold"
                style={{
                  background: "var(--gradient-primary)",
                  color: "var(--primary-foreground)",
                  boxShadow: "var(--shadow-neu-sm), 0 0 20px rgba(52,211,153,0.3)",
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
