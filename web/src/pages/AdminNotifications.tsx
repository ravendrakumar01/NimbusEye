/**
 * Notification profiles.
 *
 * A profile answers four questions: who is told, through what, how soon, and what
 * happens when nobody responds. The last one is the reason this screen exists —
 * the evaluated product's profile had an empty escalation ladder, which means a
 * real outage at 3am notifies one channel once and then nothing further happens.
 *
 * The banner at the top reports whether delivery is possible at all, because a
 * fully configured routing policy with no enabled channel sends nothing, and that
 * is not discoverable from this page otherwise.
 */

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, ArrowDown, Check, Plus, Trash2 } from "lucide-react";
import { Link } from "react-router-dom";

import { api } from "../lib/api";
import type { AlertRule, EscalationLevel, NotificationChannel, NotificationProfile } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { cx, since } from "../lib/format";
import {
  Button,
  Card,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
} from "../components/ui";

/** The severities an alert can carry, worst first. */
const SEVERITIES = [
  { value: "down", label: "Down", tone: "text-st-down" },
  { value: "critical", label: "Critical", tone: "text-st-critical" },
  { value: "trouble", label: "Trouble", tone: "text-st-trouble" },
  { value: "up", label: "Recovered", tone: "text-st-up" },
];

function ChannelPicker({
  all,
  selected,
  onChange,
  disabled,
}: {
  all: NotificationChannel[];
  selected: string[];
  onChange: (ids: string[]) => void;
  disabled: boolean;
}) {
  if (all.length === 0) {
    return <span className="text-[12px] text-slate-400">no channels exist</span>;
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {all.map((c) => {
        const on = selected.includes(c.id);
        return (
          <button
            key={c.id}
            type="button"
            disabled={disabled || !c.enabled}
            onClick={() => onChange(on ? selected.filter((x) => x !== c.id) : [...selected, c.id])}
            title={c.enabled ? undefined : "This channel is disabled, so it cannot be routed to"}
            className={cx(
              "rounded border px-2 py-0.5 text-[12px] transition",
              !c.enabled
                ? "cursor-not-allowed border-slate-200 bg-slate-50 text-slate-400 line-through"
                : on
                  ? "border-brand-600 bg-brand-50 text-brand-700"
                  : "border-slate-300 bg-white text-slate-600 hover:bg-slate-50",
            )}
          >
            {on && <Check className="mr-1 inline size-3" aria-hidden="true" />}
            {c.display_name}
          </button>
        );
      })}
    </div>
  );
}

function ProfileCard({
  profile,
  channels,
  canEdit,
  onSaved,
}: {
  profile: NotificationProfile;
  channels: NotificationChannel[];
  canEdit: boolean;
  onSaved: () => void;
}) {
  const [rules, setRules] = useState<AlertRule[]>(profile.alert_rules);
  const [levels, setLevels] = useState<EscalationLevel[]>(profile.escalation_levels);
  const [delay, setDelay] = useState(profile.notification_delay);
  const [repeat, setRepeat] = useState(profile.persistent_alert_interval);
  const [onRecovery, setOnRecovery] = useState(profile.notify_on_recovery);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setRules(profile.alert_rules);
    setLevels(profile.escalation_levels);
    setDelay(profile.notification_delay);
    setRepeat(profile.persistent_alert_interval);
    setOnRecovery(profile.notify_on_recovery);
    setSaved(false);
  }, [profile]);

  const channelsFor = (sev: string) => rules.find((r) => r.severity === sev)?.channels ?? [];
  const setChannelsFor = (sev: string, ids: string[]) => {
    const rest = rules.filter((r) => r.severity !== sev);
    setRules(ids.length === 0 ? rest : [...rest, { severity: sev, channels: ids }]);
  };

  const dirty =
    JSON.stringify(rules) !== JSON.stringify(profile.alert_rules) ||
    JSON.stringify(levels) !== JSON.stringify(profile.escalation_levels) ||
    delay !== profile.notification_delay ||
    repeat !== profile.persistent_alert_interval ||
    onRecovery !== profile.notify_on_recovery;

  const save = useCallback(async () => {
    setSaving(true);
    setError(null);
    try {
      await api.updateNotificationProfile(profile.id, {
        alert_rules: rules,
        escalation_levels: levels,
        notification_delay: delay,
        persistent_alert_interval: repeat,
        notify_on_recovery: onRecovery,
      });
      setSaved(true);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }, [profile.id, rules, levels, delay, repeat, onRecovery, onSaved]);

  const routed = rules.reduce((n, r) => n + r.channels.length, 0);

  return (
    <Card
      title={profile.display_name}
      action={
        <span className="text-[11px] text-slate-400">
          {profile.is_default && "default · "}updated {since(profile.updated_at)}
        </span>
      }
    >
      <div className="space-y-4 px-3 pt-2 pb-3">
        {routed === 0 && (
          <InfoBanner tone="warn">
            No severity routes to a channel, so this profile records alerts and tells nobody.
          </InfoBanner>
        )}

        <div>
          <div className="mb-1.5 text-[12px] font-medium text-slate-600">Route each severity</div>
          <div className="space-y-2">
            {SEVERITIES.map((s) => (
              <div key={s.value} className="flex flex-wrap items-center gap-3">
                <span className={cx("w-20 shrink-0 text-[13px] font-medium", s.tone)}>{s.label}</span>
                <ChannelPicker
                  all={channels}
                  selected={channelsFor(s.value)}
                  disabled={!canEdit}
                  onChange={(ids) => setChannelsFor(s.value, ids)}
                />
              </div>
            ))}
          </div>
        </div>

        <div className="flex flex-wrap items-end gap-5 border-t border-slate-100 pt-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500" title="Confirmed failures before anyone is told">
              Notify after
            </span>
            <span className="flex items-center gap-1.5">
              <input
                type="number"
                min={0}
                max={60}
                value={delay}
                disabled={!canEdit}
                onChange={(e) => setDelay(Number(e.target.value))}
                className="w-16 rounded border border-slate-300 px-1.5 py-0.5 text-right text-[13px] tabular-nums"
              />
              <span className="text-[12px] text-slate-500">polls</span>
            </span>
          </label>

          <label className="flex flex-col gap-1">
            <span className="text-[12px] text-slate-500" title="0 disables repeats">
              Repeat while open
            </span>
            <span className="flex items-center gap-1.5">
              <input
                type="number"
                min={0}
                max={1440}
                value={repeat}
                disabled={!canEdit}
                onChange={(e) => setRepeat(Number(e.target.value))}
                className="w-20 rounded border border-slate-300 px-1.5 py-0.5 text-right text-[13px] tabular-nums"
              />
              <span className="text-[12px] text-slate-500">
                {repeat === 0 ? "never repeat" : "minutes"}
              </span>
            </span>
          </label>

          <label className="flex items-center gap-2 pb-1 text-[13px] text-slate-700">
            <input
              type="checkbox"
              checked={onRecovery}
              disabled={!canEdit}
              onChange={(e) => setOnRecovery(e.target.checked)}
              className="size-3.5"
            />
            Tell them when it recovers
          </label>
        </div>

        <div className="border-t border-slate-100 pt-3">
          <div className="mb-1.5 flex items-center justify-between">
            <span className="text-[12px] font-medium text-slate-600">
              Escalation — when nobody acknowledges
            </span>
            {canEdit && (
              <Button
                size="xs"
                onClick={() =>
                  setLevels([
                    ...levels,
                    {
                      level: levels.length + 1,
                      after_minutes: (levels.at(-1)?.after_minutes ?? 0) + 15,
                      channels: [],
                    },
                  ])
                }
              >
                <Plus className="size-3" aria-hidden="true" />
                Add level
              </Button>
            )}
          </div>

          {levels.length === 0 ? (
            <InfoBanner tone="warn">
              Nothing escalates. An alert nobody acknowledges is notified once and then sits there —
              which is how an outage runs overnight with an alarm already open.
            </InfoBanner>
          ) : (
            <div className="space-y-2">
              {levels.map((l, i) => (
                <div key={i} className="flex flex-wrap items-center gap-2 rounded border border-slate-200 p-2">
                  {i > 0 && <ArrowDown className="size-3 text-slate-300" aria-hidden="true" />}
                  <span className="text-[12px] text-slate-500">after</span>
                  <input
                    type="number"
                    min={1}
                    max={1440}
                    value={l.after_minutes}
                    disabled={!canEdit}
                    onChange={(e) =>
                      setLevels(levels.map((x, j) => (j === i ? { ...x, after_minutes: Number(e.target.value) } : x)))
                    }
                    className="w-16 rounded border border-slate-300 px-1.5 py-0.5 text-right text-[13px] tabular-nums"
                  />
                  <span className="text-[12px] text-slate-500">min, tell</span>
                  <ChannelPicker
                    all={channels}
                    selected={l.channels}
                    disabled={!canEdit}
                    onChange={(ids) => setLevels(levels.map((x, j) => (j === i ? { ...x, channels: ids } : x)))}
                  />
                  {canEdit && (
                    <Button size="xs" variant="ghost" onClick={() => setLevels(levels.filter((_, j) => j !== i))}>
                      <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
                      <span className="sr-only">Remove level {i + 1}</span>
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>

        {error && (
          <InfoBanner tone="warn">
            <span className="flex items-start gap-1.5">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </span>
          </InfoBanner>
        )}

        {canEdit && (
          <div className="flex items-center gap-2 border-t border-slate-100 pt-3">
            <Button variant="primary" disabled={!dirty || saving} onClick={save}>
              {saving ? "Saving…" : "Save profile"}
            </Button>
            {saved && !dirty && (
              <span className="flex items-center gap-1 text-[12px] text-st-up">
                <Check className="size-3.5" aria-hidden="true" />
                Saved
              </span>
            )}
            {dirty && <span className="text-[12px] text-slate-500">unsaved changes</span>}
          </div>
        )}
      </div>
    </Card>
  );
}

export function AdminNotifications() {
  const { user } = useAuth();
  const canEdit = user.role === "owner" || user.role === "admin";
  const profiles = useAsync(() => api.notificationProfiles(), []);
  const channels = useAsync(() => api.channels(), []);

  const reload = useCallback(() => {
    profiles.reload();
    channels.reload();
  }, [profiles, channels]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Notification Profiles"
        meta={profiles.data ? `${profiles.data.profiles.length} profiles` : undefined}
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {profiles.data && !profiles.data.delivery_ready && (
          <InfoBanner tone="warn">
            There is no enabled notification channel, so nothing here can deliver. Alerts will still
            be raised and visible on the Alarms page, but no one will be told.{" "}
            <Link to="/admin/channels" className="font-medium text-brand-600 hover:underline">
              Add a channel
            </Link>
            .
          </InfoBanner>
        )}

        {profiles.initialLoading || channels.initialLoading ? (
          <Spinner label="Loading profiles" />
        ) : profiles.error ? (
          <ErrorState error={profiles.error} onRetry={reload} />
        ) : (
          (profiles.data?.profiles ?? []).map((p) => (
            <ProfileCard
              key={p.id}
              profile={p}
              channels={channels.data?.channels ?? []}
              canEdit={canEdit}
              onSaved={reload}
            />
          ))
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          <span className="font-medium">Notify after</span> counts confirmed poll failures, not
          minutes, so it scales with each monitor's own check interval.{" "}
          <span className="font-medium">Repeat while open</span> is what stops an unacknowledged
          alert from going quiet, and the escalation ladder is what moves it to someone else when
          the first person does not respond.
        </p>
      </div>
    </div>
  );
}
