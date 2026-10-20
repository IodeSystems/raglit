import type { ReactNode } from "react";

// InlineMarkdown renders the inline Markdown a model writes into a summary —
// `code`, **bold**, *emphasis* — as elements, so a reader sees the words and
// not the punctuation. Text nodes only; nothing is parsed as HTML.
const TOKEN = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\s][^*\n]*\*)/g;

export function InlineMarkdown({ text }: { text: string }) {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const t = m[0];
    if (t.startsWith("`")) out.push(<code key={at}>{t.slice(1, -1)}</code>);
    else if (t.startsWith("**")) out.push(<strong key={at}>{t.slice(2, -2)}</strong>);
    else out.push(<em key={at}>{t.slice(1, -1)}</em>);
    last = at + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return <>{out}</>;
}
