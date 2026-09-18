/**
 * Honest placeholder for sections that are not built yet.
 *
 * It lists what the section will contain rather than showing an empty shell or a
 * fake chart, so a reviewer can tell at a glance what is real and what is not.
 */

import { Construction } from "lucide-react";
import { Panel } from "../components/ui";

export function Placeholder({
  title,
  summary,
  planned,
}: {
  title: string;
  summary: string;
  planned: string[];
}) {
  return (
    <Panel title={title} subtitle="Not implemented yet">
      <div className="flex flex-col items-start gap-4 sm:flex-row">
        <span
          className="grid size-10 shrink-0 place-items-center rounded-lg bg-st-discovery-bg text-st-discovery"
          aria-hidden="true"
        >
          <Construction className="size-5" />
        </span>
        <div className="min-w-0">
          <p className="text-sm text-slate-700">{summary}</p>
          <p className="mt-3 text-xs font-medium tracking-wide text-slate-500 uppercase">
            Planned for this section
          </p>
          <ul className="mt-2 space-y-1.5">
            {planned.map((p) => (
              <li key={p} className="flex gap-2 text-sm text-slate-600">
                <span aria-hidden="true" className="mt-1.5 size-1.5 shrink-0 rounded-full bg-slate-300" />
                <span>{p}</span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </Panel>
  );
}
