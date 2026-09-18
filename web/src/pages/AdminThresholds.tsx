/**
 * Threshold profiles.
 *
 * A profile binds alerting rules to a resource *type*, not to a resource. One
 * profile governs every Block Volume in the estate, which is why the row shows how
 * many monitors it covers: editing a number that affects 56 volumes should not
 * look the same as editing one that affects nothing.
 *
 * Two things the editor deliberately will not let you do, because the server
 * refuses them too and a form that offers an option the API rejects is worse than
 * one that never offers it:
 *
 *   Choose a direction. Whether high or low is the problem is a property of the
 *   metric — free disk space and CPU are both percentages and read opposite ways.
 *   The comparison operator is shown, never edited.
 *
 *   Save a rule with neither threshold set. That stores a rule which is displayed
 *   as active and never evaluated, which is the most dangerous state a monitoring
 *   configuration can be in.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Check, Plus, RotateCcw, Trash2, X } from "lucide-react";

import { api } from "../lib/api";
import type { ThresholdProfile, ThresholdRule } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useAuth } from "../lib/auth";
import { cx, since } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Select,
  Spinner,
} from "../components/ui";

const PROVIDERS = [
  { value: "", label: "All providers" },
  { value: "oci", label: "OCI" },
  { value: "aws", label: "AWS" },
  { value: "azure", label: "Azure" },
  { value: "gcp", label: "GCP" },
  { value: "synthetic", label: "Synthetic checks" },
  { value: "kubernetes", label: "Kubernetes" },
];

/** Numeric input that distinguishes "not set" from zero. */
function NumField({
  value,
  onChange,
  unit,
  placeholder,
  disabled,
}: {
  value: number | null;
  onChange: (v: number | null) => void;
  unit?: string;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <input
        type="number"
        inputMode="decimal"
        disabled={disabled}
        value={value === null ? "" : value}
        placeholder={placeholder ?? "not set"}
        onChange={(e) => onChange(e.target.value === "" ? null : Number(e.target.value))}
        className={cx(
          "w-20 rounded border px-1.5 py-0.5 text-right text-[13px] tabular-nums",
          disabled ? "border-slate-200 bg-slate-50 text-slate-400" : "border-slate-300 text-slate-800",
        )}
      />
      {unit && <span className="text-[11px] text-slate-400">{unit}</span>}
    </span>
  );
}

function RuleRow({
  rule,
  onChange,
  onRemove,
  readOnly,
}: {
  rule: ThresholdRule;
  onChange: (r: ThresholdRule) => void;
  onRemove: () => void;
  readOnly: boolean;
}) {
  // Stating the direction in words next to the operator: ">= 90" is ambiguous
  // until you know whether 90 is a ceiling or a floor.
  const direction = rule.higher_is_worse ? "higher is worse" : "lower is worse";
  return (
    <tr className="border-b border-slate-100 last:border-0">
      <td className="px-3 py-2">
        <div className="text-[13px] font-medium text-slate-800">{rule.label ?? rule.metric}</div>
        <div className="text-[11px] text-slate-400">
          {rule.metric} · {direction}
        </div>
      </td>
      <td className="px-3 py-2 text-center">
        <span
          className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[12px] text-slate-600"
          title="The comparison follows from the metric, so it cannot be edited here"
        >
          {rule.op}
        </span>
      </td>
      <td className="px-3 py-2">
        <NumField
          value={rule.trouble}
          unit={rule.unit}
          disabled={readOnly}
          onChange={(v) => onChange({ ...rule, trouble: v })}
        />
      </td>
      <td className="px-3 py-2">
        <NumField
          value={rule.critical}
          unit={rule.unit}
          disabled={readOnly}
          onChange={(v) => onChange({ ...rule, critical: v })}
        />
      </td>
      <td className="px-3 py-2">
        <NumField
          value={rule.polls_check}
          disabled={readOnly}
          placeholder="3"
          onChange={(v) => onChange({ ...rule, polls_check: v ?? 1 })}
        />
      </td>
      <td className="px-3 py-2 text-right">
        {!readOnly && (
          <Button size="xs" variant="ghost" onClick={onRemove}>
            <Trash2 className="size-3.5 text-slate-400" aria-hidden="true" />
            <span className="sr-only">Remove {rule.label ?? rule.metric}</span>
          </Button>
        )}
      </td>
    </tr>
  );
}

function ProfileEditor({
  id,
  onClose,
  onSaved,
  canEdit,
}: {
  id: string;
  onClose: () => void;
  onSaved: () => void;
  canEdit: boolean;
}) {
  const state = useAsync(() => api.thresholdProfile(id), [id]);
  const [rules, setRules] = useState<ThresholdRule[]>([]);
  const [downPolls, setDownPolls] = useState(2);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Seed the form once the profile arrives, and whenever it is reloaded.
  useEffect(() => {
    if (state.data) {
      setRules(state.data.rules);
      setDownPolls(state.data.down_polls_check);
      setError(null);
      setSaved(false);
    }
  }, [state.data]);

  const p = state.data;
  const dirty = useMemo(() => {
    if (!p) return false;
    return (
      JSON.stringify(rules) !== JSON.stringify(p.rules) || downPolls !== p.down_polls_check
    );
  }, [p, rules, downPolls]);

  const save = useCallback(async () => {
    setSaving(true);
    setError(null);
    try {
      await api.updateThresholdProfile(id, { rules, down_polls_check: downPolls });
      setSaved(true);
      onSaved();
      state.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }, [id, rules, downPolls, onSaved, state]);

  if (state.initialLoading) return <Spinner label="Loading the profile" />;
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  if (!p) return null;

  const unconfigured = (p.available_metrics ?? []).filter(
    (m) => !rules.some((r) => r.metric === m.metric),
  );

  return (
    <div className="space-y-3">
      <Card
        title={p.display_name}
        action={
          <div className="flex items-center gap-2">
            {dirty && (
              <Button size="xs" variant="ghost" onClick={() => { setRules(p.rules); setDownPolls(p.down_polls_check); }}>
                <RotateCcw className="size-3.5" aria-hidden="true" />
                Discard
              </Button>
            )}
            <Button size="xs" onClick={onClose}>
              <X className="size-3.5" aria-hidden="true" />
              Close
            </Button>
          </div>
        }
      >
        <div className="flex flex-wrap items-center gap-x-6 gap-y-1 px-3 pt-1 pb-3 text-[12px] text-slate-500">
          <span>
            {p.type_name} · {p.provider.toUpperCase()}
          </span>
          <span>
            governs{" "}
            <span className={cx("font-medium", p.resource_count > 0 ? "text-slate-800" : "text-slate-400")}>
              {p.resource_count}
            </span>{" "}
            {p.resource_count === 1 ? "monitor" : "monitors"}
          </span>
          <span>updated {since(p.updated_at)}</span>
          {p.system_generated && <span className="text-slate-400">created by the system</span>}
        </div>

        {p.resource_count === 0 && (
          <div className="px-3 pb-3">
            <InfoBanner>
              Nothing of this type is currently monitored, so these rules govern nothing yet. They
              will apply automatically when a {p.type_name} is discovered.
            </InfoBanner>
          </div>
        )}

        <div className="overflow-x-auto border-t border-slate-200">
          <table className="w-full border-collapse">
            <thead>
              <tr className="bg-slate-50">
                <th scope="col" className="px-3 py-2 text-left text-[12px] font-medium text-slate-600">
                  Metric
                </th>
                <th scope="col" className="px-3 py-2 text-center text-[12px] font-medium text-slate-600">
                  When
                </th>
                <th scope="col" className="px-3 py-2 text-left text-[12px] font-medium text-slate-600">
                  Trouble
                </th>
                <th scope="col" className="px-3 py-2 text-left text-[12px] font-medium text-slate-600">
                  Critical
                </th>
                <th
                  scope="col"
                  className="px-3 py-2 text-left text-[12px] font-medium text-slate-600"
                  title="How many consecutive readings must agree before an alert opens"
                >
                  Polls to confirm
                </th>
                <th scope="col" className="w-10" />
              </tr>
            </thead>
            <tbody>
              {rules.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center text-[13px] text-slate-500">
                    No metric rules. This type is still monitored for up and down, but nothing else
                    will raise an alert.
                  </td>
                </tr>
              ) : (
                rules.map((r, i) => (
                  <RuleRow
                    key={r.metric}
                    rule={r}
                    readOnly={!canEdit}
                    onChange={(next) => setRules(rules.map((x, j) => (j === i ? next : x)))}
                    onRemove={() => setRules(rules.filter((_, j) => j !== i))}
                  />
                ))
              )}
            </tbody>
          </table>
        </div>

        <div className="flex flex-wrap items-center gap-3 border-t border-slate-200 px-3 py-2.5">
          <label className="flex items-center gap-2 text-[13px] text-slate-700">
            Failed polls before the monitor counts as down
            <NumField value={downPolls} disabled={!canEdit} onChange={(v) => setDownPolls(v ?? 1)} />
          </label>
        </div>

        {unconfigured.length > 0 && canEdit && (
          <div className="border-t border-slate-200 px-3 py-2.5">
            <div className="mb-1.5 text-[12px] text-slate-500">
              Metrics this type reports but the profile does not watch:
            </div>
            <div className="flex flex-wrap gap-1.5">
              {unconfigured.map((m) => (
                <Button key={m.metric} size="xs" onClick={() => setRules([...rules, m])}>
                  <Plus className="size-3" aria-hidden="true" />
                  {m.label ?? m.metric}
                </Button>
              ))}
            </div>
          </div>
        )}

        {error && (
          <div className="border-t border-slate-200 px-3 py-2.5">
            <InfoBanner tone="warn">
              <span className="flex items-start gap-1.5">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                {error}
              </span>
            </InfoBanner>
          </div>
        )}

        {canEdit && (
          <div className="flex items-center gap-2 border-t border-slate-200 px-3 py-2.5">
            <Button variant="primary" disabled={!dirty || saving} onClick={save}>
              {saving ? "Saving…" : "Save thresholds"}
            </Button>
            {saved && !dirty && (
              <span className="flex items-center gap-1 text-[12px] text-st-up">
                <Check className="size-3.5" aria-hidden="true" />
                Saved. The alerter picks this up on its next pass.
              </span>
            )}
            {dirty && <span className="text-[12px] text-slate-500">unsaved changes</span>}
          </div>
        )}
      </Card>
    </div>
  );
}

export function AdminThresholds() {
  const { user } = useAuth();
  const canEdit = user.role === "owner" || user.role === "admin";
  const [provider, setProvider] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const state = useAsync(() => api.thresholdProfiles(provider || undefined), [provider]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Threshold Profiles"
        meta={state.data ? `${state.data.count} profiles` : undefined}
        actions={
          <Select
            value={provider}
            onChange={setProvider}
            options={PROVIDERS}
            label="Provider"
          />
        }
      />

      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
        {!canEdit && (
          <InfoBanner>
            You are signed in as {user.role}. Thresholds are visible so you can see what governs an
            alert, but changing them needs the admin or owner role.
          </InfoBanner>
        )}

        {open && (
          <ProfileEditor
            id={open}
            canEdit={canEdit}
            onClose={() => setOpen(null)}
            onSaved={state.reload}
          />
        )}

        {state.initialLoading ? (
          <Spinner label="Loading profiles" />
        ) : state.error ? (
          <ErrorState error={state.error} onRetry={state.reload} />
        ) : !state.data || state.data.profiles.length === 0 ? (
          <EmptyState title="No threshold profiles" />
        ) : (
          <Card title="One profile per resource type">
            <div className="overflow-x-auto">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="bg-slate-50">
                    <th scope="col" className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600">
                      Profile
                    </th>
                    <th scope="col" className="border-b border-slate-200 px-3 py-2 text-left text-[12px] font-medium text-slate-600">
                      Provider
                    </th>
                    <th scope="col" className="border-b border-slate-200 px-3 py-2 text-right text-[12px] font-medium text-slate-600">
                      Rules
                    </th>
                    <th scope="col" className="border-b border-slate-200 px-3 py-2 text-right text-[12px] font-medium text-slate-600">
                      Monitors governed
                    </th>
                    <th scope="col" className="border-b border-slate-200 px-3 py-2 text-right text-[12px] font-medium text-slate-600">
                      Down after
                    </th>
                    <th scope="col" className="border-b border-slate-200 px-3 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {state.data.profiles.map((p: ThresholdProfile, i) => (
                    <tr
                      key={p.id}
                      className={cx(i % 2 ? "bg-slate-50/60" : undefined, open === p.id && "bg-brand-50")}
                    >
                      <td className="px-3 py-2">
                        <button
                          type="button"
                          onClick={() => setOpen(open === p.id ? null : p.id)}
                          className="text-left text-[13px] text-brand-600 hover:underline"
                        >
                          {p.type_name}
                        </button>
                        <div className="text-[11px] text-slate-400">{p.resource_type}</div>
                      </td>
                      <td className="px-3 py-2 text-[13px] text-slate-600">{p.provider.toUpperCase()}</td>
                      <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-700">
                        {p.rules.length || <span className="text-slate-300">none</span>}
                      </td>
                      <td className="px-3 py-2 text-right text-[13px] tabular-nums">
                        <span className={p.resource_count > 0 ? "font-medium text-slate-800" : "text-slate-300"}>
                          {p.resource_count || "—"}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-right text-[13px] tabular-nums text-slate-600">
                        {p.down_polls_check} polls
                      </td>
                      <td className="px-3 py-2 text-right">
                        <Button size="xs" onClick={() => setOpen(open === p.id ? null : p.id)}>
                          {open === p.id ? "Close" : canEdit ? "Edit" : "View"}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}

        <p className="px-1 text-[11px] leading-relaxed text-slate-500">
          Thresholds attach to a resource type, so one profile covers every monitor of that type —
          the <span className="font-medium">Monitors governed</span> column is how many that is
          right now. <span className="font-medium">Polls to confirm</span> is the number of
          consecutive readings that must agree before an alert opens; cloud metrics arrive late and
          out of order, so alerting on a single sample produces constant false alarms.
        </p>
      </div>
    </div>
  );
}
