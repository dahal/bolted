"use client";

import { useState } from "react";

// CopyCommand renders a one-line shell snippet that the user can copy with
// one click anywhere on the row. Used on the landing page wherever the
// fastest path forward is "run this".
export function CopyCommand({ command, ariaLabel }: { command: string; ariaLabel?: string }) {
  const [copied, setCopied] = useState(false);

  return (
    <button
      type="button"
      aria-label={ariaLabel ?? `Copy command: ${command}`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(command);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // navigator.clipboard can fail under insecure contexts (no https,
          // no localhost). Quiet fallback: the command is still selectable
          // visually so the user can copy by hand.
        }
      }}
      className="group flex w-full items-center gap-3 border border-fd-border bg-fd-card/40 px-4 py-3 text-left font-mono text-sm transition-colors hover:border-fd-primary/40 hover:bg-fd-card focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-fd-ring"
    >
      <span className="select-none text-fd-primary" aria-hidden="true">
        $
      </span>
      <span className="flex-1 truncate text-fd-foreground">{command}</span>
      <span
        aria-live="polite"
        className="font-mono text-xs uppercase tracking-widest text-fd-muted-foreground transition-opacity group-hover:text-fd-primary"
      >
        {copied ? "✓ copied" : "click to copy"}
      </span>
    </button>
  );
}
