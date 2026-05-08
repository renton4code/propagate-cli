import { useEffect, useMemo, useRef, useState } from "react";

// ─── Types ────────────────────────────────────────────────────────────────────

export type TerminalLine =
  | { kind: "cmd";    text: string }
  | { kind: "output"; text: string }
  | { kind: "blank" };

export type TerminalDef = {
  step:  string;
  label: string;
  user:  string;
  dir:   string;
  lines: TerminalLine[];
};

// ─── Weight / timing ─────────────────────────────────────────────────────────

function lineWeight(line: TerminalLine): number {
  if (line.kind === "blank")  return 2;
  if (line.kind === "output") return 3;
  return Math.max(10, line.text.length * 0.9);
}

function buildWeights(text: string): number[] {
  return Array.from(text).map((c, i) => {
    let base = 1;
    if (c === " ")              base = 1.6;
    else if (",;:".includes(c)) base = 2.2;
    else if (".!?".includes(c)) base = 3.0;
    const jitter = 0.7 + ((i * 9301 + 49297) % 233) / 233 * 0.8;
    return base * jitter;
  });
}

function charsAt(text: string, t: number): number {
  if (t <= 0) return 0;
  if (t >= 1) return text.length;
  const w     = buildWeights(text);
  const total = w.reduce((a, b) => a + b, 0);
  const target = t * total;
  let acc = 0;
  for (let i = 0; i < w.length; i++) {
    acc += w[i];
    if (acc >= target) return i + 1;
  }
  return text.length;
}

// ─── Core computation ─────────────────────────────────────────────────────────

type LineState =
  | { kind: "hidden" }
  | { kind: "partial"; text: string }
  | { kind: "full";    text: string };

/**
 * Returns line states AND exactly one `activeCmdLine` index — the single line
 * that owns the caret. During typing this is the partial cmd line. Between cmd
 * lines (or when all done) it is the last fully-revealed cmd line (idle cursor).
 */
function computeLines(
  lines: TerminalLine[],
  rawT: number,
): { states: LineState[]; activeCmdLine: number } {
  const TYPING_START = 0.05;
  const TYPING_END   = 0.88;
  const t = Math.max(0, Math.min(1, (rawT - TYPING_START) / (TYPING_END - TYPING_START)));

  const total     = lines.reduce((s, l) => s + lineWeight(l), 0);
  const consumed  = t * total;
  let   acc       = 0;

  let partialCmd = -1;  // cmd line currently being typed
  let lastFullCmd = -1; // last cmd line that finished

  const states: LineState[] = lines.map((line, i) => {
    const w    = lineWeight(line);
    const prev = acc;
    acc += w;

    if (consumed <= prev) return { kind: "hidden" };

    const within = Math.min(1, (consumed - prev) / w);

    if (line.kind === "blank") {
      return within >= 0.5 ? { kind: "full", text: "" } : { kind: "hidden" };
    }

    if (line.kind === "output") {
      return within > 0 ? { kind: "full", text: line.text } : { kind: "hidden" };
    }

    // cmd line
    const revealed = charsAt(line.text, within);
    if (revealed === line.text) {
      lastFullCmd = i;
      return { kind: "full", text: line.text };
    }
    partialCmd = i;
    return { kind: "partial", text: line.text.slice(0, revealed) };
  });

  // Exactly one active line: the typing one if present, otherwise the last finished cmd.
  const activeCmdLine = partialCmd !== -1 ? partialCmd : lastFullCmd;

  return { states, activeCmdLine };
}

// ─── Sub-components ───────────────────────────────────────────────────────────

/** Single blinking cursor — rendered exactly once per terminal. */
function Caret() {
  return (
    <span
      aria-hidden
      style={{
        display:       "inline-block",
        width:         "0.5ch",
        height:        "0.78em",
        marginLeft:    "1px",
        verticalAlign: "-0.05em",
        background:    "var(--primary)",
        animation:     "termBlink 1.1s ease-in-out infinite",
        boxShadow:     "0 0 5px var(--primary)",
        willChange:    "opacity",
      }}
    />
  );
}

function TerminalLineRow({
  line,
  state,
  hasCursor,
}: {
  line:      TerminalLine;
  state:     LineState;
  hasCursor: boolean;
}) {
  if (state.kind === "hidden") return null;

  if (line.kind === "blank") return <div style={{ height: "0.75rem" }} />;

  const text     = state.text ?? "";
  const isCheck  = text.startsWith("✓");
  const isWarn   = text.startsWith("!");
  const isIndent = text.startsWith("  ");

  const color =
    line.kind === "cmd" ? "var(--foreground)"       :
    isCheck              ? "var(--primary)"          :
    isWarn               ? "oklch(0.78 0.16 80)"     :
    isIndent             ? "var(--muted-foreground)" :
                           "var(--foreground)";

  return (
    <div
      className="font-mono text-sm leading-relaxed"
      style={{ display: "flex", alignItems: "baseline", columnGap: 0 }}
    >
      {line.kind === "cmd" && (
        <span style={{ color: "var(--primary)", marginRight: "0.5ch", flexShrink: 0 }}>$</span>
      )}
      <span style={{ color, opacity: line.kind === "output" ? 0.9 : 1 }}>
        {text}
        {hasCursor && <Caret />}
      </span>
    </div>
  );
}

function ScrollHint({ visible }: { visible: boolean }) {
  return (
    <div
      style={{
        position:   "absolute",
        bottom:     "2rem",
        left:       "50%",
        transform:  "translateX(-50%)",
        display:    "flex",
        flexDirection: "column",
        alignItems: "center",
        gap:        "0.25rem",
        fontSize:   "0.75rem",
        color:      "var(--muted-foreground)",
        opacity:    visible ? 1 : 0,
        transition: "opacity 0.5s ease",
        pointerEvents: "none",
        whiteSpace: "nowrap",
      }}
    >
      <span>scroll to continue</span>
      <svg
        width="16" height="16" viewBox="0 0 16 16" fill="none"
        style={{ animation: visible ? "scrollBounce 1.2s ease-in-out infinite" : "none" }}
      >
        <path
          d="M8 3v10M4 9l4 4 4-4"
          stroke="currentColor" strokeWidth="1.5"
          strokeLinecap="round" strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}

function TerminalWindow({
  def,
  phaseProgress,
  totalSteps,
}: {
  def:          TerminalDef;
  phaseProgress: number;
  totalSteps:   number;
}) {
  const { states, activeCmdLine } = useMemo(
    () => computeLines(def.lines, phaseProgress),
    [def.lines, phaseProgress],
  );

  const allDone = phaseProgress >= 0.88;

  return (
    <div style={{ width: "100%", maxWidth: "42rem", margin: "0 auto", position: "relative" }}>
      {/* Step badge */}
      <div style={{ display: "flex", alignItems: "center", gap: "0.75rem", marginBottom: "1rem" }}>
        <span
          className="font-mono"
          style={{
            fontSize:   "0.7rem",
            padding:    "0.2rem 0.6rem",
            borderRadius: "0.375rem",
            background: "rgba(52,211,153,0.08)",
            border:     "1px solid rgba(52,211,153,0.2)",
            color:      "var(--primary)",
          }}
        >
          {def.step} / {String(totalSteps).padStart(2, "0")}
        </span>
        <span style={{ fontSize: "0.875rem", color: "var(--muted-foreground)" }}>
          {def.label}
        </span>
      </div>

      {/* Terminal window */}
      <div
        style={{
          borderRadius: "0.75rem",
          overflow:     "hidden",
          background:   "var(--surface)",
          border:       "1px solid var(--border)",
          boxShadow:    "0 24px 60px -16px rgba(0,0,0,0.6), var(--shadow-neu)",
        }}
      >
        {/* Title bar */}
        <div
          style={{
            display:       "flex",
            alignItems:    "center",
            gap:           "0.5rem",
            padding:       "0.75rem 1rem",
            borderBottom:  "1px solid var(--border)",
          }}
        >
          <span style={{ width: "0.75rem", height: "0.75rem", borderRadius: "50%", background: "oklch(0.65 0.2 25)", flexShrink: 0 }} />
          <span style={{ width: "0.75rem", height: "0.75rem", borderRadius: "50%", background: "oklch(0.78 0.16 80)", flexShrink: 0 }} />
          <span style={{ width: "0.75rem", height: "0.75rem", borderRadius: "50%", background: "oklch(0.72 0.18 150)", flexShrink: 0 }} />
          <div
            className="font-mono"
            style={{
              marginLeft:  "0.75rem",
              fontSize:    "0.75rem",
              display:     "flex",
              gap:         "0.25rem",
              color:       "var(--muted-foreground)",
            }}
          >
            <span style={{ color: "var(--primary)" }}>{def.user}</span>
            <span>:</span>
            <span>{def.dir}</span>
          </div>
        </div>

        {/* Body */}
        <div style={{ padding: "1.25rem", minHeight: "16rem", display: "flex", flexDirection: "column", gap: "0.125rem" }}>
          {def.lines.map((line, i) => (
            <TerminalLineRow
              key={i}
              line={line}
              state={states[i]}
              hasCursor={i === activeCmdLine}
            />
          ))}
        </div>
      </div>

      <ScrollHint visible={allDone} />
    </div>
  );
}

// ─── Main export ──────────────────────────────────────────────────────────────

const SCROLL_PER_TERMINAL = 300;

export function TerminalFlow({ terminals }: { terminals: TerminalDef[] }) {
  const n          = terminals.length;
  const wrapperRef = useRef<HTMLDivElement>(null);
  const barFillRef = useRef<HTMLDivElement>(null);   // progress bar — direct DOM update
  const rafRef     = useRef<number>(0);

  const [view, setView] = useState({ index: 0, phase: 0, opacity: 1 });

  useEffect(() => {
    const compute = () => {
      const el = wrapperRef.current;
      if (!el) return;

      const rect     = el.getBoundingClientRect();
      const total    = el.offsetHeight - window.innerHeight;
      const scrolled = Math.min(Math.max(-rect.top, 0), total);
      const progress = total > 0 ? scrolled / total : 0;

      // Progress bar: direct DOM — zero React overhead, zero CSS transition lag.
      if (barFillRef.current) {
        barFillRef.current.style.width = `${progress * 100}%`;
      }

      const rawIndex = progress * n;
      const index    = Math.min(Math.floor(rawIndex), n - 1);
      const phase    = rawIndex - Math.floor(rawIndex);

      const opacity =
        index === 0 && phase < 0.05 ? phase / 0.05 :
        phase > 0.93                ? Math.max(0, 1 - (phase - 0.93) / 0.07) :
        1;

      setView(prev => {
        // Skip re-render when the display state is unchanged (char-level resolution).
        if (
          prev.index === index &&
          prev.opacity === opacity &&
          Math.abs(prev.phase - phase) < 0.001
        ) return prev;
        return { index, phase, opacity };
      });
    };

    const onScroll = () => {
      cancelAnimationFrame(rafRef.current);
      rafRef.current = requestAnimationFrame(compute);
    };

    compute();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", compute);
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", compute);
      cancelAnimationFrame(rafRef.current);
    };
  }, [n]);

  const { index, phase, opacity } = view;
  const slideY = phase > 0.93 ? ((phase - 0.93) / 0.07) * 20 : 0;

  return (
    <div ref={wrapperRef} style={{ position: "relative", height: `${n * SCROLL_PER_TERMINAL}vh` }}>
      {/* Progress bar — filled by direct DOM ref, no React re-render */}
      <div
        style={{
          position:   "sticky",
          top:        "3.5rem",
          zIndex:     40,
          height:     "2px",
          width:      "100%",
          background: "var(--neu-dark)",
        }}
      >
        <div
          ref={barFillRef}
          style={{
            height:     "100%",
            width:      "0%",
            background: "var(--primary)",
            boxShadow:  "0 0 10px var(--primary)",
            willChange: "width",
          }}
        />
      </div>

      {/* Sticky viewport */}
      <div
        style={{
          position:       "sticky",
          top:            "3.5rem",
          height:         "calc(100vh - 3.5rem)",
          display:        "flex",
          alignItems:     "center",
          justifyContent: "center",
          padding:        "0 1.5rem",
          overflow:       "hidden",
        }}
      >
        <div aria-hidden={true} className="mesh-bg -z-10" />

        <div
          style={{
            width:     "100%",
            opacity,
            transform: `translateY(${slideY}px)`,
            willChange: "opacity, transform",
          }}
        >
          <TerminalWindow
            key={index}
            def={terminals[index]}
            phaseProgress={phase}
            totalSteps={n}
          />
        </div>
      </div>

      <style>{`
        @keyframes termBlink {
          0%, 45%   { opacity: 1; }
          55%, 100% { opacity: 0; }
        }
        @keyframes scrollBounce {
          0%, 100% { transform: translateY(0);   }
          50%      { transform: translateY(5px); }
        }
      `}</style>
    </div>
  );
}
