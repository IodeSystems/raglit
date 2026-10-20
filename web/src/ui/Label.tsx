import { AI_SUMMARY_LABEL, AI_TITLE_LABEL, aiTitleTooltip } from "./display";

// The single mark above machine-written text. Shown ONCE per caption or
// summary; the text itself carries the .snip.generated style, not a second
// warning.
export function AISummaryLabel() {
  return <div className="ailabel">{AI_SUMMARY_LABEL}</div>;
}

// A title a model wrote. The model name is in the tooltip, not in the line.
export function AITitleLabel({ model }: { model?: string }) {
  return (
    <span className="ailabel inline" title={aiTitleTooltip(model)}>
      {AI_TITLE_LABEL}
    </span>
  );
}
