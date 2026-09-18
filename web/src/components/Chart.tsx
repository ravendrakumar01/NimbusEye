/**
 * Metric chart: a line chart with threshold guides, drawn as plain SVG.
 *
 * Written rather than imported for two reasons. A charting library is 50-150 kB
 * for one chart type, and none of them draw the thing that matters most here —
 * a threshold guide that the series is compared against — without fighting the
 * API. The chart's whole job is to answer "is this above the line", so the line
 * is a first-class element, not an annotation.
 */

import { useId, useMemo, useState } from "react";
import type { MetricSeries } from "../lib/api";
import { absolute, cx, metricValue } from "../lib/format";

interface Geometry {
  w: number;
  h: number;
  padL: number;
  padR: number;
  padT: number;
  padB: number;
}

const GEO: Geometry = { w: 640, h: 180, padL: 52, padR: 12, padT: 12, padB: 24 };

export function MetricChart({
  series,
  height = 180,
  className,
}: {
  series: MetricSeries;
  height?: number;
  className?: string;
}) {
  const gradientId = useId();
  const [hover, setHover] = useState<number | null>(null);

  const model = useMemo(() => build(series), [series]);

  if (!model) {
    return (
      <div
        className="flex items-center justify-center text-xs text-slate-400"
        style={{ height }}
      >
        No data in this window
      </div>
    );
  }

  const { pts, xOf, yOf, yTicks, xTicks, min, max, breachedAt } = model;
  const { w, h, padL, padR, padT, padB } = GEO;

  const line = pts.map((p, i) => `${i === 0 ? "M" : "L"}${xOf(i)},${yOf(p.v)}`).join(" ");
  const area = `${line} L${xOf(pts.length - 1)},${h - padB} L${xOf(0)},${h - padB} Z`;

  const hovered = hover !== null ? pts[hover] : undefined;

  // Colour the series by whether it is currently in breach. A chart that stays
  // blue while the value sits above the critical line is actively misleading.
  const tone = breachedAt === "critical" ? "#d93025" : breachedAt === "trouble" ? "#e0a800" : "#1565c0";

  return (
    <div className={cx("relative", className)}>
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        className="w-full touch-none"
        style={{ height }}
        role="img"
        aria-label={`${series.label} over time, currently ${metricValue(pts[pts.length - 1]?.v, series.unit)}`}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const rect = e.currentTarget.getBoundingClientRect();
          const xRatio = (e.clientX - rect.left) / rect.width;
          const x = xRatio * w;
          if (x < padL || x > w - padR) {
            setHover(null);
            return;
          }
          const i = Math.round(((x - padL) / (w - padL - padR)) * (pts.length - 1));
          setHover(Math.max(0, Math.min(pts.length - 1, i)));
        }}
      >
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={tone} stopOpacity="0.18" />
            <stop offset="100%" stopColor={tone} stopOpacity="0" />
          </linearGradient>
        </defs>

        {/* Horizontal grid and y labels */}
        {yTicks.map((t) => (
          <g key={t}>
            <line
              x1={padL}
              x2={w - padR}
              y1={yOf(t)}
              y2={yOf(t)}
              stroke="#eef1f4"
              strokeWidth="1"
              vectorEffect="non-scaling-stroke"
            />
            <text x={padL - 6} y={yOf(t) + 3} textAnchor="end" className="fill-slate-400 text-[9px]">
              {metricValue(t, series.unit)}
            </text>
          </g>
        ))}

        {/* Threshold guides. Dashed and labelled so they read as limits, not data. */}
        {(["trouble", "critical"] as const).map((kind) => {
          const v = kind === "trouble" ? series.trouble : series.critical;
          if (v === undefined || v === null || v < min || v > max) return null;
          const colour = kind === "critical" ? "#d93025" : "#e0a800";
          return (
            <g key={kind}>
              <line
                x1={padL}
                x2={w - padR}
                y1={yOf(v)}
                y2={yOf(v)}
                stroke={colour}
                strokeWidth="1"
                strokeDasharray="4 3"
                vectorEffect="non-scaling-stroke"
              />
              <text x={w - padR - 2} y={yOf(v) - 3} textAnchor="end" style={{ fill: colour }} className="text-[9px]">
                {kind === "critical" ? "Critical" : "Trouble"}
              </text>
            </g>
          );
        })}

        <path d={area} fill={`url(#${gradientId})`} />
        <path
          d={line}
          fill="none"
          stroke={tone}
          strokeWidth="1.5"
          vectorEffect="non-scaling-stroke"
          strokeLinejoin="round"
        />

        {/* X labels */}
        {xTicks.map(({ i, label }) => (
          <text
            key={i}
            x={xOf(i)}
            y={h - padB + 14}
            textAnchor="middle"
            className="fill-slate-400 text-[9px]"
          >
            {label}
          </text>
        ))}

        {/* Hover guide */}
        {hover !== null && hovered && (
          <g>
            <line
              x1={xOf(hover)}
              x2={xOf(hover)}
              y1={padT}
              y2={h - padB}
              stroke="#94a3b8"
              strokeWidth="1"
              strokeDasharray="3 2"
              vectorEffect="non-scaling-stroke"
            />
            <circle cx={xOf(hover)} cy={yOf(hovered.v)} r="3" fill={tone} />
          </g>
        )}
      </svg>

      {hover !== null && hovered && (
        <div
          className="pointer-events-none absolute top-1 rounded border border-slate-200 bg-white px-2 py-1 text-[11px] shadow-sm"
          style={{
            left: `${(xOf(hover) / GEO.w) * 100}%`,
            transform: "translateX(-50%)",
          }}
        >
          <div className="num font-medium text-slate-800">
            {metricValue(hovered.v, series.unit)}
          </div>
          <div className="text-slate-500">{absolute(hovered.t)}</div>
        </div>
      )}
    </div>
  );
}

function build(series: MetricSeries) {
  const pts = series.samples;
  if (pts.length < 2) return null;

  const values = pts.map((p) => p.v);
  let lo = Math.min(...values);
  let hi = Math.max(...values);

  // Include thresholds in the domain when they are near the data, so the guide is
  // visible. A threshold orders of magnitude away is excluded rather than
  // flattening the series into a straight line.
  for (const t of [series.trouble, series.critical]) {
    if (t === undefined || t === null) continue;
    if (t > hi && t < hi * 4) hi = t;
    if (t < lo && t > lo / 4) lo = t;
  }

  if (series.unit === "percent") {
    lo = Math.min(lo, 0);
    hi = Math.max(hi, 100);
  }
  if (hi === lo) {
    hi = lo + 1;
  }
  // Headroom so the line never touches the frame.
  const pad = (hi - lo) * 0.08;
  const min = series.unit === "percent" ? 0 : Math.max(0, lo - pad);
  const max = series.unit === "percent" ? 100 : hi + pad;

  const { w, h, padL, padR, padT, padB } = GEO;
  const xOf = (i: number) => padL + (i / (pts.length - 1)) * (w - padL - padR);
  const yOf = (v: number) => {
    const clamped = Math.max(min, Math.min(max, v));
    return h - padB - ((clamped - min) / (max - min)) * (h - padT - padB);
  };

  const yTicks = [0, 0.25, 0.5, 0.75, 1].map((f) => min + (max - min) * f);

  const tickCount = 5;
  const xTicks = Array.from({ length: tickCount }, (_, k) => {
    const i = Math.round((k / (tickCount - 1)) * (pts.length - 1));
    const d = new Date(pts[i]!.t);
    return {
      i,
      label: d.toLocaleTimeString("en-IN", { hour: "2-digit", minute: "2-digit", hour12: false }),
    };
  });

  // Current breach state, from the most recent sample.
  const last = values[values.length - 1]!;
  const higherIsWorse =
    series.critical !== undefined && series.trouble !== undefined && series.critical !== null && series.trouble !== null
      ? series.critical > series.trouble
      : true;
  let breachedAt: "critical" | "trouble" | null = null;
  const crit = series.critical;
  const trb = series.trouble;
  if (crit !== undefined && crit !== null && (higherIsWorse ? last >= crit : last <= crit)) {
    breachedAt = "critical";
  } else if (trb !== undefined && trb !== null && (higherIsWorse ? last >= trb : last <= trb)) {
    breachedAt = "trouble";
  }

  return { pts, xOf, yOf, yTicks, xTicks, min, max, breachedAt };
}
