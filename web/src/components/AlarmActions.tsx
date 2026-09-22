/**
 * Alarm actions: the controls the reference console puts in the Alarms header, and
 * the per-alarm ones that go with them.
 *
 * Each action is deliberately distinct, and the wording says which is which,
 * because the difference is the whole point:
 *
 *	Acknowledge — I have seen this. Stops nothing.
 *	Mute — stop telling me, keep showing me. Needs a duration and a reason.
 *	Resolve — I have decided this is over. Needs a reason, because the evaluator
 *	          did not see it end.
 *	Maintenance — this is planned, exclude it from availability too.
 */

import { useCallback, useState } from "react";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  BellOff,
  BellRing,
  CalendarClock,
  Check,
  MessageSquarePlus,
  X,
} from "lucide-react";

import { api } from "../lib/api";
import type { Alarm } from "../lib/api";
import { absolute, cx, since } from "../lib/format";
import { Button, InfoBanner, Select } from "./ui";

const MUTE_DURATIONS = [
  { value: "30", label: "30 minutes" },
  { value: "60", label: "1 hour" },
  { value: "240", label: "4 hours" },
  { value: "720", label: "12 hours" },
  { value: "1440", label: "1 day" },
  { value: "4320", label: "3 days" },
];

/** Whether a mute is still in force. */
export function isMuted(a: Alarm): boolean {
  return !!a.muted_until && Date.parse(a.muted_until) > Date.now();
}

/**
 * Delivery state for one alarm, as a short phrase.
 *
 * This column exists because the Alarms page otherwise says only that a problem
 * was detected, and stays silent on whether anybody was told — which is the part
 * that matters at 3am.
 */
export function DeliveryState({ alarm }: { alarm: Alarm }) {
  if (alarm.suppressed_by_maintenance) {
    return (
      <span
        className="text-[11px] text-slate-500"
        title="Inside a maintenance window: recorded, not delivered, and excluded from availability"
      >
        in maintenance
      </span>
    );
  }
  if (isMuted(alarm)) {
    return (
      <span
        className="inline-flex items-center gap-1 text-[11px] text-st-trouble"
        title={alarm.mute_reason ? `Muted: ${alarm.mute_reason}` : "Muted"}
      >
        <BellOff className="size-3" aria-hidden="true" />
        muted until {since(alarm.muted_until!)}
      </span>
    );
  }
  if (alarm.notified > 0) {
    return (
      <span
        className="inline-flex items-center gap-1 text-[11px] text-st-up"
        title={alarm.last_notified_at ? `Last sent ${absolute(alarm.last_notified_at)}` : undefined}
      >
        <Check className="size-3" aria-hidden="true" />
        {alarm.notified} sent
      </span>
    );
  }
  return (
    <Link
      to="/alert-logs"
      className="text-[11px] text-st-trouble hover:underline"
      title="Nothing has been delivered for this alarm. The Alert Logs page says why."
    >
      not delivered
    </Link>
  );
}

/** Root-cause notes, shown inline under an alarm. */
export function RCANotes({ alarm }: { alarm: Alarm }) {
  if (!alarm.rca || alarm.rca.length === 0) return null;
  return (
    <ul className="mt-1 space-y-0.5">
      {alarm.rca.map((n, i) => (
        <li key={i} className="text-[11px] text-slate-500">
          <span className="text-slate-400" title={absolute(n.at)}>
            {since(n.at)}
          </span>{" "}
          — {n.note}
        </li>
      ))}
    </ul>
  );
}

/* -------------------------------------------------------------------------- */
/* Action panels                                                               */
/* -------------------------------------------------------------------------- */

export function MutePanel({
  alarms,
  onDone,
  onCancel,
}: {
  alarms: Alarm[];
  onDone: () => void;
  onCancel: () => void;
}) {
  const [minutes, setMinutes] = useState("60");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      for (const a of alarms) {
        await api.muteAlarm(a.id, Number(minutes), reason.trim());
      }
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [alarms, minutes, reason, onDone]);

  return (
    <div className="border-b border-slate-200 bg-slate-50/80 px-5 py-3">
      <div className="mb-2 flex items-center gap-2">
        <BellOff className="size-3.5 text-slate-500" aria-hidden="true" />
        <span className="text-[13px] font-medium text-slate-700">
          Mute {alarms.length} {alarms.length === 1 ? "alarm" : "alarms"}
        </span>
        <Button size="xs" className="ml-auto" onClick={onCancel}>
          <X className="size-3.5" aria-hidden="true" />
          Cancel
        </Button>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-[12px] text-slate-500">For</span>
          <Select value={minutes} onChange={setMinutes} options={MUTE_DURATIONS} label="Duration" />
        </label>
        <label className="flex flex-1 flex-col gap-1">
          <span className="text-[12px] text-slate-500">Why</span>
          <input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Storage expansion ordered, ETA Thursday"
            className="w-full rounded border border-slate-300 px-2 py-1 text-[13px]"
          />
        </label>
        <Button variant="primary" disabled={busy || !reason.trim()} onClick={submit}>
          {busy ? "Muting…" : "Mute"}
        </Button>
      </div>
      <p className="mt-2 text-[11px] text-slate-500">
        The alarm stays open and visible; only delivery stops. A reason is required because
        silence without one is indistinguishable from the alerting being broken. Longest
        mute is 7 days — for anything beyond that,{" "}
        <Link to="/maintenance" className="text-brand-600 hover:underline">
          schedule maintenance
        </Link>{" "}
        instead, which also excludes the downtime from availability.
      </p>
      {error && (
        <div className="mt-2">
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </span>
          </InfoBanner>
        </div>
      )}
    </div>
  );
}

export function ResolvePanel({
  alarm,
  onDone,
  onCancel,
}: {
  alarm: Alarm;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      await api.resolveAlarm(alarm.id, reason.trim());
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [alarm.id, reason, onDone]);

  return (
    <div className="space-y-2 bg-slate-50/80 px-3 py-2.5">
      <div className="text-[12px] text-slate-600">
        Close <span className="font-medium">{alarm.resource_name}</span> by hand. The
        evaluator has not seen this condition end, so the record needs to say who decided
        it had — and any open outage for this monitor is closed with the same reason.
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Retention policy applied, storage reclaimed"
          className="min-w-64 flex-1 rounded border border-slate-300 px-2 py-1 text-[13px]"
        />
        <Button variant="primary" disabled={busy || !reason.trim()} onClick={submit}>
          {busy ? "Closing…" : "Close alarm"}
        </Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
      {error && (
        <InfoBanner tone="warn">
          <span className="flex items-start gap-1.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            {error}
          </span>
        </InfoBanner>
      )}
    </div>
  );
}

export function NotePanel({
  alarm,
  onDone,
  onCancel,
}: {
  alarm: Alarm;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      await api.annotateAlarm(alarm.id, note.trim());
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }, [alarm.id, note, onDone]);

  return (
    <div className="space-y-2 bg-slate-50/80 px-3 py-2.5">
      <div className="text-[12px] text-slate-600">
        What did this turn out to be? The value is in the second occurrence — the same
        alarm in six weeks, with a note saying what it was last time.
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <input
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Audit log retention was never set; storage grows until it is"
          className="min-w-64 flex-1 rounded border border-slate-300 px-2 py-1 text-[13px]"
        />
        <Button variant="primary" disabled={busy || !note.trim()} onClick={submit}>
          {busy ? "Saving…" : "Add note"}
        </Button>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
      {error && (
        <InfoBanner tone="warn">
          <span className="flex items-start gap-1.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            {error}
          </span>
        </InfoBanner>
      )}
    </div>
  );
}

/** The per-alarm action row. */
export function AlarmActions({
  alarm,
  onAction,
  onMute,
  onResolve,
  onNote,
  busy,
}: {
  alarm: Alarm;
  onAction: (fn: () => Promise<unknown>) => void;
  onMute: () => void;
  onResolve: () => void;
  onNote: () => void;
  busy: boolean;
}) {
  const closed = alarm.state === "resolved";
  return (
    <div className="flex flex-wrap items-center justify-end gap-1">
      {!closed && !alarm.acknowledged_at && (
        <Button size="xs" disabled={busy} onClick={() => onAction(() => api.acknowledge(alarm.id))}>
          Acknowledge
        </Button>
      )}
      {!closed &&
        (isMuted(alarm) ? (
          <Button
            size="xs"
            disabled={busy}
            title="Resume delivery for this alarm"
            onClick={() => onAction(() => api.unmuteAlarm(alarm.id))}
          >
            <BellRing className="size-3.5" aria-hidden="true" />
            Unmute
          </Button>
        ) : (
          <Button size="xs" disabled={busy} onClick={onMute} title="Stop telling me, keep showing me">
            <BellOff className="size-3.5" aria-hidden="true" />
            Mute
          </Button>
        ))}
      {!closed && (
        <Link to={`/maintenance?monitor=${alarm.resource_id}`}>
          <Button size="xs" title="Planned work: suppress and exclude from availability">
            <CalendarClock className="size-3.5" aria-hidden="true" />
            Maintenance
          </Button>
        </Link>
      )}
      <Button size="xs" disabled={busy} onClick={onNote} title="Record what this turned out to be">
        <MessageSquarePlus className="size-3.5" aria-hidden="true" />
        Note
      </Button>
      {!closed && (
        <Button size="xs" variant="danger" disabled={busy} onClick={onResolve}>
          Close
        </Button>
      )}
    </div>
  );
}

/** Live indicator: a dot that pulses while polling is active. */
export function LiveDot({ active }: { active: boolean }) {
  return (
    <span
      className="inline-flex items-center gap-1 text-[11px] font-medium text-slate-500"
      title={active ? "Refreshing every 30 seconds" : "Paused while the tab is hidden"}
    >
      <span
        className={cx(
          "size-1.5 rounded-full",
          active ? "animate-pulse bg-st-up" : "bg-slate-300",
        )}
      />
      Live
    </span>
  );
}
