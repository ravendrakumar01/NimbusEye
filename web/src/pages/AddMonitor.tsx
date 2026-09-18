/**
 * Add Monitor — the catalog of what can be created, and the form to create it.
 *
 * Two things this page is deliberate about:
 *
 * 1. It shows what cannot be created here, and why. The catalog has 46 monitor
 *    types but only 8 can be added by hand; the other 38 are discovered from a
 *    connected cloud account. Hiding them would leave an operator hunting for
 *    "Add EC2 Instance" and concluding the feature is missing.
 * 2. The form is generated from the API's field specification, so the validation
 *    rules and the inputs cannot disagree — there is one definition of what a
 *    Website monitor needs, and it lives in the backend.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  Activity,
  Calendar,
  ChevronRight,
  Cloud,
  Globe,
  Heart,
  Lock,
  Network,
  Plug,
  Search,
  Signal,
} from "lucide-react";
import { ApiError, api } from "../lib/api";
import type { CreatableType, MonitorInput } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { CATEGORY_LABEL, PROVIDER_LABEL, cx } from "../lib/format";
import {
  Button,
  Card,
  EmptyState,
  ErrorState,
  InfoBanner,
  PageHeader,
  Spinner,
  TabStrip,
} from "../components/ui";

const ICONS: Record<string, typeof Globe> = {
  globe: Globe,
  api: Plug,
  plug: Plug,
  signal: Signal,
  dns: Network,
  lock: Lock,
  calendar: Calendar,
  heart: Heart,
};

export function AddMonitor() {
  const [params, setParams] = useSearchParams();
  const selected = params.get("type") ?? "";
  const tab = (params.get("tab") ?? "create") as "create" | "discovered";
  const [search, setSearch] = useState("");

  const types = useAsync(() => api.creatableTypes(), []);

  const chosen = useMemo(
    () => types.data?.creatable.find((t) => t.code === selected),
    [types.data, selected],
  );

  function choose(code: string | null) {
    const next = new URLSearchParams(params);
    if (code) next.set("type", code);
    else next.delete("type");
    setParams(next, { replace: true });
  }

  if (types.initialLoading) return <Spinner label="Loading monitor types" />;
  if (types.error) return <ErrorState error={types.error} onRetry={types.reload} />;
  if (!types.data) return null;

  if (chosen) {
    return <MonitorForm type={chosen} onBack={() => choose(null)} />;
  }

  const q = search.trim().toLowerCase();
  const creatable = types.data.creatable.filter(
    (t) => !q || t.display_name.toLowerCase().includes(q) || t.description.toLowerCase().includes(q),
  );
  const discovered = types.data.discovered.filter(
    (t) => !q || t.display_name.toLowerCase().includes(q),
  );

  // Group the discovered-only types by provider so the reason reads once per group.
  const byProvider = discovered.reduce<Record<string, typeof discovered>>((acc, t) => {
    (acc[t.provider] ??= []).push(t);
    return acc;
  }, {});

  return (
    <>
      <PageHeader
        title="Add Monitor"
        actions={
          <>
            <label className="relative">
              <span className="sr-only">Search monitor types</span>
              <Search
                className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-slate-400"
                aria-hidden="true"
              />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search types"
                className="w-48 rounded border border-slate-300 py-1 pr-2 pl-7 text-xs placeholder:text-slate-400"
              />
            </label>
            <Link to="/">
              <Button size="xs">Monitor List</Button>
            </Link>
          </>
        }
      />

      <div className="border-b border-slate-200 px-5">
        <TabStrip
          label="Monitor sources"
          value={tab}
          onChange={(v) => {
            const next = new URLSearchParams(params);
            if (v === "create") next.delete("tab");
            else next.set("tab", v);
            setParams(next, { replace: true });
          }}
          tabs={[
            { value: "create", label: "Create Here", count: creatable.length },
            { value: "discovered", label: "Discovered from Cloud", count: discovered.length },
          ]}
        />
      </div>

      <div className="px-5 py-4">
        {tab === "create" ? (
          creatable.length === 0 ? (
            <Card className="py-6">
              <EmptyState title="No monitor types match that search" />
            </Card>
          ) : (
            <>
              <p className="mb-4 max-w-3xl text-[13px] text-slate-600">
                These checks are run by NimbusEye itself, so they need no cloud credentials and
                start reporting within a minute of being created.
              </p>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                {creatable.map((t) => {
                  const Icon = ICONS[t.icon] ?? Activity;
                  return (
                    <button
                      key={t.code}
                      type="button"
                      onClick={() => choose(t.code)}
                      className="flex flex-col items-start rounded border border-slate-200 bg-white p-4 text-left transition hover:border-brand-400 hover:shadow-sm"
                    >
                      <span className="grid size-9 place-items-center rounded-full bg-brand-50 text-brand-600">
                        <Icon className="size-4.5" aria-hidden="true" />
                      </span>
                      <span className="mt-2.5 text-[13px] font-medium text-slate-800">
                        {t.display_name}
                      </span>
                      <span className="mt-1 text-[11px] leading-snug text-slate-500">
                        {t.description}
                      </span>
                      <span className="mt-2 text-[11px] text-brand-500">Configure →</span>
                    </button>
                  );
                })}
              </div>
            </>
          )
        ) : (
          <div className="space-y-4">
            <InfoBanner>
              These types cannot be added by hand. They appear automatically once the cloud account
              or cluster they live in is connected, because a hand-created cloud resource would
              show up in every count while measuring nothing.
            </InfoBanner>
            {Object.entries(byProvider).map(([provider, list]) => (
              <Card key={provider} className="px-4 py-3">
                <div className="flex items-center gap-2">
                  <Cloud className="size-4 text-slate-400" aria-hidden="true" />
                  <h2 className="text-[13px] font-medium text-slate-800">
                    {PROVIDER_LABEL[provider] ?? provider}
                  </h2>
                  <span className="text-[11px] text-slate-500">{list[0]?.reason}</span>
                  <Link
                    to="/admin/cloud-accounts"
                    className="ml-auto text-[12px] text-brand-500 hover:underline"
                  >
                    Connect an account →
                  </Link>
                </div>
                <div className="mt-2.5 flex flex-wrap gap-1.5">
                  {list.map((t) => (
                    <span
                      key={t.code}
                      className="rounded border border-slate-200 px-2 py-0.5 text-[11px] text-slate-600"
                      title={CATEGORY_LABEL[t.category] ?? t.category}
                    >
                      {t.display_name}
                    </span>
                  ))}
                </div>
              </Card>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

/* ------------------------------------------------------------------- form */

function MonitorForm({ type, onBack }: { type: CreatableType; onBack: () => void }) {
  const navigate = useNavigate();
  const [values, setValues] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    if (type.default_poll_sec) init.check_interval_sec = String(type.default_poll_sec);
    return init;
  });
  const [name, setName] = useState("");
  const [tagText, setTagText] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [banner, setBanner] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  function set(key: string, v: string) {
    setValues((cur) => ({ ...cur, [key]: v }));
  }

  async function submit() {
    setSaving(true);
    setErrors({});
    setBanner(null);

    // Parsed here rather than sent as free text so the API receives the shapes it
    // declares: status codes as numbers, tags as a map.
    const input: MonitorInput = {
      resource_type: type.code,
      display_name: name,
      target: values.target ?? "",
      check_interval_sec: intOrUndefined(values.check_interval_sec),
      timeout_sec: intOrUndefined(values.timeout_sec),
      port: intOrUndefined(values.port),
      method: values.method || undefined,
      match_text: values.match_text || undefined,
      record_type: values.record_type || undefined,
      resolver: values.resolver || undefined,
      expected_ip: values.expected_ip || undefined,
      expected_status: (values.expected_status ?? "")
        .split(",")
        .map((s) => Number(s.trim()))
        .filter((n) => Number.isFinite(n) && n > 0),
      tags: parseTags(tagText),
    };

    try {
      const res = await api.createMonitor(input);
      // Straight to the monitor: the first thing anyone wants after creating a
      // check is to see whether it passes.
      navigate(`/monitor/${res.id}`);
    } catch (e) {
      if (e instanceof ApiError && e.fields) {
        setErrors(e.fields);
        setBanner("Some fields need attention.");
      } else {
        setBanner(e instanceof Error ? e.message : "Could not create the monitor");
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-1.5">
            <button type="button" onClick={onBack} className="text-brand-500 hover:underline">
              Add Monitor
            </button>
            <ChevronRight className="size-3.5 text-slate-400" aria-hidden="true" />
            <span className="text-slate-800">{type.display_name}</span>
          </span>
        }
        actions={
          <Button size="xs" onClick={onBack}>
            Back to types
          </Button>
        }
      />

      <div className="max-w-3xl px-5 py-4">
        <p className="mb-4 text-[13px] text-slate-600">{type.description}</p>

        {banner && (
          <div className="mb-4 rounded border border-st-down/30 bg-st-down-bg px-3 py-2 text-[13px] text-slate-700">
            {banner}
          </div>
        )}

        <Card className="space-y-4 px-4 py-4">
          <Field
            label="Display Name"
            value={name}
            onChange={setName}
            error={errors.display_name}
            placeholder={`e.g. ${exampleName(type.code)}`}
            required
          />

          {type.fields.map((f) => (
            <Field
              key={f.key}
              label={f.label}
              value={values[f.key] ?? ""}
              onChange={(v) => set(f.key, v)}
              error={errors[f.key]}
              placeholder={f.placeholder}
              help={f.help}
              required={f.required}
              mono={f.key === "target" || f.key === "resolver" || f.key === "expected_ip"}
            />
          ))}

          <Field
            label="Tags"
            value={tagText}
            onChange={setTagText}
            placeholder="env=prod, owner=platform"
            help="Comma-separated key=value pairs. Used for grouping and filtering."
          />

          <div className="flex justify-end gap-2 border-t border-slate-200 pt-3">
            <Button onClick={onBack}>Cancel</Button>
            <Button variant="primary" onClick={submit} disabled={saving}>
              {saving ? "Creating…" : "Create Monitor"}
            </Button>
          </div>
        </Card>

        <p className="mt-3 text-[11px] text-slate-500">
          The monitor starts in the <strong>Discovery</strong> state and moves to Up or Down after
          its first check. Nothing is reported as healthy until it has actually been measured.
        </p>
      </div>
    </>
  );
}

function Field({
  label,
  value,
  onChange,
  error,
  placeholder,
  help,
  required,
  mono,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  placeholder?: string;
  help?: string;
  required?: boolean;
  mono?: boolean;
}) {
  return (
    <label className="block">
      <span className="block text-[12px] font-medium text-slate-700">
        {label} {required && <span className="text-st-down">*</span>}
      </span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-invalid={error ? true : undefined}
        className={cx(
          "mt-1 w-full rounded border px-2.5 py-1.5 text-[13px] placeholder:text-slate-400",
          mono && "font-mono text-[12px]",
          error ? "border-st-down bg-st-down-bg/40" : "border-slate-300",
        )}
      />
      {error ? (
        <p className="mt-1 text-[11px] text-st-down">{error}</p>
      ) : help ? (
        <p className="mt-1 text-[11px] text-slate-500">{help}</p>
      ) : null}
    </label>
  );
}

function intOrUndefined(v: string | undefined): number | undefined {
  if (!v) return undefined;
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

function parseTags(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const part of text.split(",")) {
    const [k, ...rest] = part.split("=");
    const key = (k ?? "").trim();
    if (key) out[key] = rest.join("=").trim();
  }
  return out;
}

function exampleName(code: string): string {
  switch (code) {
    case "WEB_HTTP":
      return "Corporate Website";
    case "WEB_REST_API":
      return "Public API health";
    case "WEB_PING":
      return "VPN concentrator";
    case "WEB_PORT":
      return "SMTP relay";
    case "WEB_DNS":
      return "example.com A record";
    case "WEB_SSL_CERT":
      return "portal.example.com certificate";
    case "WEB_DOMAIN_EXPIRY":
      return "example.com registration";
    default:
      return "Nightly backup";
  }
}
