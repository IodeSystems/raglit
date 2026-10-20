import type { ReactNode } from "react";

import { humanizeError } from "./display";

// A collapsed "Technical details" disclosure. Raw daemon text, CLI commands and
// ids go in here, out of the main view but one click away for whoever has to
// fix it. Native <details>: keyboard and screen-reader behaviour for free.
export function Details({
  children,
  summary = "Technical details",
  open,
}: {
  children: ReactNode;
  summary?: string;
  open?: boolean;
}) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <details className="techdetails" open={open}>
      <summary>{summary}</summary>
      <div className="techdetails-body">{children}</div>
    </details>
  );
}

// ErrorText is the decided error presentation: one plain sentence, the raw text
// inside a collapsed disclosure. `extra` (for example a CLI command that fixes
// it) goes inside the disclosure too, never in the main line.
export function ErrorText({ raw, extra }: { raw: string | undefined | null; extra?: ReactNode }) {
  const { summary, detail } = humanizeError(raw);
  if (!detail) return null;
  return (
    <div className="errline">
      <div className="errline-summary">{summary}</div>
      <Details>
        <pre>{detail}</pre>
        {extra}
      </Details>
    </div>
  );
}
