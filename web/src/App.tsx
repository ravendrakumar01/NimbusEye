import { useCallback, useEffect, useState } from "react";
import { Navigate, Route, Routes, useNavigate } from "react-router-dom";
import { api, setUnauthenticatedHandler } from "./lib/api";
import type { AuthUser } from "./lib/api";
import { useAsync } from "./lib/hooks";
import { Splash } from "./components/Splash";
import { Spinner } from "./components/ui";
import { Login } from "./pages/Login";
import { ResetPassword } from "./pages/ResetPassword";
import { AuthProvider } from "./lib/auth";
import { Shell } from "./components/Shell";
import { MonitorStatus } from "./pages/MonitorStatus";
import { Alarms } from "./pages/Alarms";
import { ResourceDetail } from "./pages/ResourceDetail";
import { AdminCloudAccounts } from "./pages/AdminCloudAccounts";
import { AddMonitor } from "./pages/AddMonitor";
import { GettingStarted } from "./pages/GettingStarted";
import { CloudAccountDetail } from "./pages/CloudAccountDetail";
import { Reports } from "./pages/Reports";
import { Outages } from "./pages/Outages";
import { Maintenance } from "./pages/Maintenance";
import { SLO } from "./pages/SLO";
import { AlertLogs } from "./pages/AlertLogs";
import { MonitorGroups } from "./pages/MonitorGroups";
import { BusinessHours, Tags } from "./pages/AdminConfig";
import { Discovered, BulkAction } from "./pages/Discovered";
import { CloudInventory, CloudServices } from "./pages/CloudInventory";
import { Kubernetes } from "./pages/Kubernetes";
import { AdminThresholds } from "./pages/AdminThresholds";
import { AdminNotifications } from "./pages/AdminNotifications";
import { AdminChannels } from "./pages/AdminChannels";
import { AdminUsers } from "./pages/AdminUsers";
import { AdminAudit } from "./pages/AdminAudit";
import { Placeholder } from "./pages/Placeholder";

/**
 * Minimum time the boot splash stays on screen.
 *
 * The requests it waits for settle in roughly 300ms, which is too brief to
 * register — the screen appeared and vanished before it could be read. Matched to
 * one full cycle of the glyph animation so the ripple completes rather than being
 * cut off mid-way.
 *
 * The splash never shows for *less* than the real work takes; this only stops it
 * being a flash when the work is fast.
 */
const MIN_SPLASH_MS = 1400;

export default function App() {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [checked, setChecked] = useState(false);
  // Held over the whole viewport after sign-in. The splash has to live above the
  // router: rendered inside a route it appears in the content area with the rail
  // and top bar around it, which is not a boot screen.
  const [booting, setBooting] = useState(false);
  const navigate = useNavigate();

  const signOutLocally = useCallback(() => setUser(null), []);

  // A 401 from anywhere drops back to the login screen, so an expired session does
  // not leave a shell full of failing requests.
  useEffect(() => {
    setUnauthenticatedHandler(signOutLocally);
  }, [signOutLocally]);

  // Asked once on load: a page refresh must not require signing in again.
  useEffect(() => {
    let cancelled = false;
    const startedAt = Date.now();
    api
      .session()
      .then((s) => {
        if (!cancelled) setUser(s.authenticated && s.user ? s.user : null);
      })
      .catch(() => {
        if (!cancelled) setUser(null);
      })
      .finally(() => {
        if (cancelled) return;
        // Held to the same minimum as the post-login splash, so a refresh does not
        // flash the wordmark for 200ms.
        const remaining = MIN_SPLASH_MS - (Date.now() - startedAt);
        if (remaining > 0) {
          window.setTimeout(() => {
            if (!cancelled) setChecked(true);
          }, remaining);
        } else {
          setChecked(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // The session check is the first thing that happens and gates everything, so
  // this is the splash the reference console shows at the same point.
  if (!checked) return <Splash />;
  // The reset link is followed by someone who cannot sign in, so it is handled
  // before the auth gate.
  if (window.location.pathname === "/reset") {
    return <ResetPassword onDone={() => window.location.replace("/")} />;
  }

  if (!user) {
    return (
      <Login
        onSignedIn={(u) => {
          setUser(u);
          setBooting(true);
          // Navigate explicitly rather than flagging state for the landing route to
          // interpret. An earlier attempt did the latter and consumed the flag in an
          // effect that ran during the loading render, so it was always false by the
          // time it was read. This also puts /getting-started in the address bar,
          // which matches the console being followed and makes the page linkable.
          navigate("/getting-started", { replace: true });

          // Warm the two requests the landing page needs, then hold the splash for
          // whichever is longer: the real work, or the minimum above.
          const startedAt = Date.now();
          void Promise.allSettled([api.summary(), api.accounts()]).then(() => {
            const remaining = MIN_SPLASH_MS - (Date.now() - startedAt);
            if (remaining > 0) {
              window.setTimeout(() => setBooting(false), remaining);
            } else {
              setBooting(false);
            }
          });
        }}
      />
    );
  }

  if (booting) return <Splash />;

  return (
    <AuthProvider value={{ user, signOut: signOutLocally }}>
    <Routes>
      <Route element={<Shell />}>
        {/* Home, Cloud, Web and Kubernetes are the same Monitor Status page at
            different scopes, exactly as in the console being replicated. */}
        {/* Landing: Monitor Status normally, Getting Started while the estate is
            empty. An empty monitor list answers "what do I do first" badly. */}
        <Route index element={<Landing />} />
        <Route path="getting-started" element={<GettingStarted />} />
        <Route
          path="cloud"
          element={
            <MonitorStatus providers={["oci", "aws", "azure", "gcp"]} providerFromUrl />
          }
        />
        <Route path="cloud/inventory" element={<CloudInventory />} />
        <Route path="cloud/services" element={<CloudServices />} />
        <Route path="web" element={<MonitorStatus providers={["synthetic"]} />} />
        <Route path="kubernetes" element={<Kubernetes />} />

        <Route path="alarms" element={<Alarms />} />

        {/* Monitor detail, reachable from any list */}
        <Route path="monitor/:id" element={<ResourceDetail />} />
        <Route path="add-monitor" element={<AddMonitor />} />

        <Route
          path="kubernetes/help"
          element={
            <Placeholder
              title="Help Assistant"
              summary="Onboarding cards for Kubernetes, Docker, server and plugin monitoring."
              planned={[
                "Add a Kubernetes monitor via an in-cluster agent manifest",
                "Docker container monitoring",
                "Server agent install for Linux and Windows",
                "Plugin integrations",
              ]}
            />
          }
        />
        <Route path="groups" element={<MonitorGroups />} />
        <Route path="reports" element={<Reports />} />
        {/* Outages has its own screen under Home as well as a reports tab. The
            data is the same; the questions differ. Home asks "what is broken
            now", reports ask "how did the period go". */}
        <Route path="outages" element={<Outages />} />
        <Route path="maintenance" element={<Maintenance />} />
        <Route path="slo" element={<SLO />} />
        <Route path="alert-logs" element={<AlertLogs />} />
        {/* Admin landing goes straight to cloud accounts: it is the only Admin
            area that is actually implemented. */}
        <Route path="admin" element={<Navigate to="/admin/cloud-accounts" replace />} />
        <Route path="admin/cloud-accounts" element={<AdminCloudAccounts />} />
        <Route path="admin/thresholds" element={<AdminThresholds />} />
        <Route path="admin/notifications" element={<AdminNotifications />} />
        <Route path="admin/channels" element={<AdminChannels />} />
        <Route path="admin/users" element={<AdminUsers />} />
        <Route path="admin/audit" element={<AdminAudit />} />
        <Route path="admin/business-hours" element={<BusinessHours />} />
        <Route path="admin/tags" element={<Tags />} />
        <Route path="admin/bulk" element={<BulkAction />} />
        <Route path="discovered" element={<Discovered />} />
        <Route path="admin/cloud-accounts/:id" element={<CloudAccountDetail />} />

        <Route path="admin/add-monitor" element={<Navigate to="/add-monitor" replace />} />

        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
    </AuthProvider>
  );
}

/**
 * Landing at "/" shows the monitor list, or onboarding while the estate is empty.
 *
 * Sign-in navigates straight to /getting-started, so this only decides what a
 * later visit to "/" shows.
 */
function Landing() {
  const summary = useAsync(() => api.summary(), []);
  if (summary.initialLoading) return <Spinner label="Loading monitors" />;
  // On error, show the monitor list rather than onboarding: a failed request is
  // not evidence that the estate is empty.
  if (summary.error) return <MonitorStatus />;
  return (summary.data?.total ?? 0) === 0 ? <GettingStarted /> : <MonitorStatus />;
}
