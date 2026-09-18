/**
 * Boot splash.
 *
 * Shown while the app resolves its session and first payload. The reference
 * console does the same thing: a centred wordmark with a row of small service
 * glyphs, on white, at the destination URL.
 *
 * It replaces a bare "Loading…" spinner for a reason beyond decoration. This
 * screen appears at exactly the moment the app cannot yet say anything true about
 * the estate, and it lasts a beat longer than a spinner feels right for — the
 * session check plus the first summary query. A branded hold reads as "starting",
 * where a lone spinner starts to read as "stuck".
 *
 * The glyphs pulse in sequence rather than all together, so it is visibly alive
 * without drawing attention. The global prefers-reduced-motion rule in index.css
 * turns the animation off for anyone who has asked for that.
 *
 * Positioned fixed over the viewport. Rendered inside a route it would appear in
 * the content area with the rail and top bar around it, which is not a boot
 * screen; this makes that mistake impossible to repeat.
 */

import { Boxes, Cloud, Database, Globe, Server } from "lucide-react";
import { cx } from "../lib/format";

/** The five domains NimbusEye watches, in the order they appear on the rail. */
const GLYPHS = [
  { icon: Cloud, label: "Cloud" },
  { icon: Server, label: "Compute" },
  { icon: Database, label: "Databases" },
  { icon: Boxes, label: "Containers" },
  { icon: Globe, label: "Websites" },
];

export function Splash({ message }: { message?: string }) {
  return (
    <div
      className="fixed inset-0 z-[70] flex flex-col items-center justify-center bg-white"
      role="status"
      aria-live="polite"
    >
      <div className="text-[42px] leading-none font-bold tracking-tight">
        <span className="text-go-500">Nimbus</span>
        <span className="text-slate-800">Eye</span>
      </div>
      <div className="mt-2 text-[15px] font-semibold text-slate-700">
        multi-cloud monitoring
      </div>

      <div className="mt-4 flex items-center gap-2">
        {GLYPHS.map(({ icon: Icon, label }, i) => (
          <span
            key={label}
            title={label}
            className="grid size-7 place-items-center rounded-full bg-go-500 text-white"
            style={{
              // Staggered so the row ripples rather than blinking as one block.
              animation: "nimbus-pulse 1.4s ease-in-out infinite",
              animationDelay: `${i * 0.14}s`,
            }}
          >
            <Icon className="size-3.5" aria-hidden="true" />
          </span>
        ))}
      </div>

      {/* The accessible name of the whole region, and a visible hint only when the
          caller has something specific to say. */}
      <span className="sr-only">Loading NimbusEye</span>
      {message && (
        <div className={cx("mt-5 text-[12px] text-slate-500")}>{message}</div>
      )}
    </div>
  );
}
