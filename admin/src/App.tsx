import { useEffect, useMemo, useRef, useState } from "react";
import {
  KeyRound,
} from "lucide-react";
import { toast } from "sonner";
import { request, storageKey } from "./api/client";
import { LocaleToggle } from "./i18n/LocaleToggle";
import { tNow, useLocale } from "./i18n/locale";
import { fillUsageSeries, naturalDayRange, usagePath } from "./lib/date";
import { hashForUser, hashForView, userIDFromHash, viewFromHash } from "./router/views";
import type {
  AdminConfig,
  AdminData,
  AppUser,
  Model,
  ModelPreset,
  ModelRoute,
  ProviderKey,
  ProviderPreset,
  ProviderRecord,
  Session,
  Stats,
  UsageSummary,
  UserAPIKey,
  UserQuotaStatus,
  View,
} from "./types/admin";
import { BrandIcon } from "./components/brand-icons";
import { Alert, AlertDescription } from "./components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "./components/ui/alert-dialog";
import { Button } from "./components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./components/ui/card";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "./components/ui/dialog";
import { Field, FieldGroup, FieldLabel } from "./components/ui/field";
import { Input } from "./components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./components/ui/select";
import { ThemeToggle } from "./theme/ThemeToggle";
import { AdminLayout } from "./layout/AdminLayout";
import { DashboardPage } from "./views/DashboardPage";
import { MyApiKeysPage } from "./views/MyApiKeysPage";
import { UsersPage } from "./views/UsersPage";
import { UserDetailPage } from "./views/UserDetailPage";
import { ProvidersPage } from "./views/ProvidersPage";
import { ModelsPage } from "./views/ModelsPage";

type ConfirmAction = {
  title: string;
  description: string;
  confirmLabel: string;
  successLabel: string;
  onConfirm: () => Promise<void>;
};

function asList<T>(value: T[] | null | undefined): T[] {
  return value ?? [];
}

function fallbackCopyText(value: string) {
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "0";
  textarea.style.top = "0";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  textarea.setSelectionRange(0, value.length);
  const ok = document.execCommand("copy");
  document.body.removeChild(textarea);
  if (!ok) throw new Error("copy failed");
}

export function App() {
  const { locale, t } = useLocale();
  const [view, setView] = useState<View>(viewFromHash());
  const [userDetailID, setUserDetailID] = useState(userIDFromHash());
  const [token, setToken] = useState(localStorage.getItem(storageKey) ?? "");
  const [loginForm, setLoginForm] = useState({ username: "", password: "" });
  const [setupRequired, setSetupRequired] = useState(false);
  const [booting, setBooting] = useState(true);
  const [session, setSession] = useState<Session | null>(null);
  const [config, setConfig] = useState<AdminConfig | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [usage, setUsage] = useState<UsageSummary | null>(null);
  const [quota, setQuota] = useState<UserQuotaStatus | null>(null);
  const [users, setUsers] = useState<AppUser[]>([]);
  const [userAPIKeys, setUserAPIKeys] = useState<UserAPIKey[]>([]);
  const [providers, setProviders] = useState<ProviderRecord[]>([]);
  const [providerKeys, setProviderKeys] = useState<ProviderKey[]>([]);
  const [providerPresets, setProviderPresets] = useState<ProviderPreset[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [modelPresets, setModelPresets] = useState<ModelPreset[]>([]);
  const [modelRoutes, setModelRoutes] = useState<ModelRoute[]>([]);
  const [version, setVersion] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [newUser, setNewUser] = useState({ username: "", password: "", role: "user" });
  const [passwordForm, setPasswordForm] = useState({ current_password: "", new_password: "" });
  const [newUserKey, setNewUserKey] = useState({ name: "", token: "" });
  const [generatedUserKey, setGeneratedUserKey] = useState<UserAPIKey | null>(null);
  const [revealedUserKeys, setRevealedUserKeys] = useState<Record<number, string>>({});
  const [copiedTarget, setCopiedTarget] = useState("");
  const copiedTimer = useRef<number | null>(null);
  const viewRefreshSeq = useRef(0);
  const [showUserDialog, setShowUserDialog] = useState(false);
  const [showPasswordDialog, setShowPasswordDialog] = useState(false);
  const [confirmAction, setConfirmAction] = useState<ConfirmAction | null>(null);
  const [usageFilter, setUsageFilter] = useState({
    key_id: "all",
    model: "all",
    ...naturalDayRange(),
  });

  async function load(activeToken = token) {
    const [nextSession, data, nextStats] = await Promise.all([
      request<Session>("/admin/api/session", activeToken),
      request<AdminData>("/admin/api/data", activeToken),
      request<Stats>("/admin/api/stats", activeToken),
    ]);
    setSession(nextSession);
    if (nextSession.version) setVersion(nextSession.version);
    setConfig(data.config);
    setUsers(asList(data.users));
    setUserAPIKeys(asList(data.user_api_keys));
    setStats(nextStats);
  }

  async function loadDashboard(activeToken = token) {
    const [nextStats, nextUserAPIKeys, nextModels, nextQuota] = await Promise.all([
      request<Stats>("/admin/api/stats", activeToken),
      request<UserAPIKey[]>("/admin/api/user-api-keys", activeToken),
      request<Model[]>("/admin/api/model-options", activeToken),
      request<UserQuotaStatus>("/admin/api/quota", activeToken),
    ]);
    setStats(nextStats);
    setUserAPIKeys(asList(nextUserAPIKeys));
    setModels(asList(nextModels));
    setQuota(nextQuota);
  }

  async function loadMyAPIKeys(activeToken = token) {
    setUserAPIKeys(asList(await request<UserAPIKey[]>("/admin/api/user-api-keys", activeToken)));
  }

  async function loadUsers(activeToken = token) {
    setUsers(asList(await request<AppUser[]>("/admin/api/users", activeToken)));
  }

  async function loadProviders(activeToken = token) {
    const [nextProviders, nextProviderPresets, nextProviderKeys] = await Promise.all([
      request<ProviderRecord[]>("/admin/api/providers", activeToken),
      request<ProviderPreset[]>("/admin/api/provider-presets", activeToken),
      request<ProviderKey[]>("/admin/api/provider-keys", activeToken),
    ]);
    setProviders(asList(nextProviders));
    setProviderPresets(asList(nextProviderPresets));
    setProviderKeys(asList(nextProviderKeys));
  }

  async function loadModels(activeToken = token) {
    const [nextProviders, nextModels, nextModelPresets, nextModelRoutes] = await Promise.all([
      request<ProviderRecord[]>("/admin/api/providers", activeToken),
      request<Model[]>("/admin/api/models", activeToken),
      request<ModelPreset[]>("/admin/api/model-presets", activeToken),
      request<ModelRoute[]>("/admin/api/model-routes", activeToken),
    ]);
    setProviders(asList(nextProviders));
    setModels(asList(nextModels));
    setModelPresets(asList(nextModelPresets));
    setModelRoutes(asList(nextModelRoutes));
  }

  async function loadView(activeView = view, activeToken = token) {
    if (!activeToken) return;
    if ((activeView === "users" || activeView === "userDetail") && session?.user.role !== "admin") return;
    if (activeView === "providers" && session?.user.role !== "admin") return;
    if (activeView === "models" && session?.user.role !== "admin") return;

    switch (activeView) {
      case "dashboard":
        await loadDashboard(activeToken);
        return;
      case "myKeys":
        await loadMyAPIKeys(activeToken);
        return;
      case "users":
        await loadUsers(activeToken);
        return;
      case "userDetail":
        return;
      case "providers":
        await loadProviders(activeToken);
        return;
      case "models":
        await loadModels(activeToken);
        return;
    }
  }

  async function login() {
    setError("");
    setLoading(true);
    try {
      const creatingInitialAdmin = setupRequired;
      const path = creatingInitialAdmin ? "/admin/api/setup" : "/admin/api/login";
      const result = await request<{ token: string; user: AppUser }>(path, "", {
        method: "POST",
        body: JSON.stringify(loginForm),
      });
      const nextToken = result.token;
      await load(nextToken);
      if (creatingInitialAdmin) setSetupRequired(false);
      localStorage.setItem(storageKey, nextToken);
      setToken(nextToken);
    } catch {
      localStorage.removeItem(storageKey);
      setError(t("auth.invalid_key"));
    } finally {
      setLoading(false);
    }
  }

  async function refresh() {
    if (!token) return;
    setLoading(true);
    try {
      await loadView();
    } finally {
      setLoading(false);
    }
  }

  async function createUser() {
    await request("/admin/api/users", token, {
      method: "POST",
      body: JSON.stringify(newUser),
    });
    setNewUser({ username: "", password: "", role: "user" });
    await refresh();
    toast.success(tNow("users.user_created"));
  }

  async function resetWeeklyQuotas() {
    try {
      await request("/admin/api/quotas/weekly/reset", token, { method: "POST" });
      await loadUsers();
      toast.success(tNow("users.weekly_quotas_reset"));
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : tNow("toast.action_failed"));
      return false;
    }
  }

  async function createUserAPIKey() {
    const created = await request<UserAPIKey>("/admin/api/user-api-keys", token, {
      method: "POST",
      body: JSON.stringify({
        name: newUserKey.name,
        valid: true,
      }),
    });
    setGeneratedUserKey(created);
    setNewUserKey({ name: "", token: "" });
    await refresh();
    toast.success(tNow("keys.generated"));
  }

  async function updateUserAPIKey(key: UserAPIKey, valid: boolean) {
    await request("/admin/api/user-api-keys", token, {
      method: "PUT",
      body: JSON.stringify({ id: key.id, valid }),
    });
    await refresh();
    toast.success(tNow(valid ? "keys.api_key_enabled" : "keys.api_key_disabled"));
  }

  async function toggleUserAPIKeyReveal(key: UserAPIKey) {
    if (revealedUserKeys[key.id]) {
      setRevealedUserKeys((current) => {
        const next = { ...current };
        delete next[key.id];
        return next;
      });
      return;
    }
    const full = await request<UserAPIKey>(`/admin/api/user-api-keys?id=${key.id}`, token);
    setRevealedUserKeys((current) => ({ ...current, [key.id]: full.token }));
  }

  async function copyUserAPIKey(key: UserAPIKey) {
    const value = revealedUserKeys[key.id]
      ? Promise.resolve(revealedUserKeys[key.id])
      : request<UserAPIKey>(`/admin/api/user-api-keys?id=${key.id}`, token).then((full) => full.token);
    await copyTextFromPromise(value, `user-key-${key.id}`);
  }

  async function deleteUserAPIKey(key: UserAPIKey) {
    await request(`/admin/api/user-api-keys?id=${key.id}`, token, { method: "DELETE" });
    await refresh();
  }

  async function copyText(value: string, target = "copy") {
    await copyTextFromPromise(Promise.resolve(value), target);
  }

  async function copyTextFromPromise(valuePromise: Promise<string>, target = "copy") {
    try {
      if (window.navigator.clipboard?.write && window.ClipboardItem && window.isSecureContext) {
        await window.navigator.clipboard.write([
          new window.ClipboardItem({
            "text/plain": valuePromise.then((value) => new Blob([value], { type: "text/plain" })),
          }),
        ]);
      } else {
        const value = await valuePromise;
        if (window.navigator.clipboard?.writeText && window.isSecureContext) {
          await window.navigator.clipboard.writeText(value);
        } else {
          fallbackCopyText(value);
        }
      }
    } catch {
      try {
        const value = await valuePromise;
        fallbackCopyText(value);
      } catch {
        toast.error(tNow("common.copy_failed"));
        return;
      }
    }
    setCopiedTarget(target);
    if (copiedTimer.current) window.clearTimeout(copiedTimer.current);
    copiedTimer.current = window.setTimeout(() => setCopiedTarget(""), 1200);
  }

  async function runConfirmAction() {
    if (!confirmAction) return;
    try {
      await confirmAction.onConfirm();
      toast.success(confirmAction.successLabel);
      setConfirmAction(null);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : tNow("toast.action_failed"));
    }
  }

  async function logout() {
    if (token) {
      await request("/admin/api/logout", token, { method: "POST" }).catch(() => undefined);
    }
    localStorage.removeItem(storageKey);
    setToken("");
    setLoginForm({ username: "", password: "" });
    setSession(null);
    setConfig(null);
    setStats(null);
    setQuota(null);
  }

  async function changePassword() {
    try {
      await request("/admin/api/password", token, {
        method: "PUT",
        body: JSON.stringify(passwordForm),
      });
      setPasswordForm({ current_password: "", new_password: "" });
      setShowPasswordDialog(false);
      toast.success(tNow("user.password_changed"));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : tNow("user.password_change_failed"));
    }
  }

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 5000);
    let active = true;

    async function boot() {
      try {
        const state = await request<{ setup_required: boolean; version?: string }>("/admin/api/setup-state", "", { signal: controller.signal });
        if (!active) return;
        setSetupRequired(state.setup_required);
        if (state.version) setVersion(state.version);
        if (token) await load(token).catch(() => logout());
      } catch {
        if (active) setError(tNow("auth.unavailable"));
      } finally {
        if (!active) return;
        window.clearTimeout(timer);
        setBooting(false);
      }
    }

    void boot();
    return () => {
      active = false;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  useEffect(() => () => {
    if (copiedTimer.current) window.clearTimeout(copiedTimer.current);
  }, []);

  useEffect(() => {
    const onHashChange = () => {
      setView(viewFromHash());
      setUserDetailID(userIDFromHash());
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  useEffect(() => {
    if (!token || !session || !config || !stats) return;
    const seq = ++viewRefreshSeq.current;
    setLoading(true);
    loadView(view, token)
      .catch((error) => {
        if (error instanceof Error && error.message === "unauthorized") {
          void logout();
          return;
        }
        toast.error(error instanceof Error ? error.message : tNow("toast.action_failed"));
      })
      .finally(() => {
        if (seq === viewRefreshSeq.current) setLoading(false);
      });
  }, [view, token, session?.user.id, session?.user.role]);

  useEffect(() => {
    if (!token || !session || view !== "dashboard") return;
    let active = true;
    setUsage(null);
    request<UsageSummary>(usagePath(usageFilter), token)
      .then((nextUsage) => { if (active) setUsage(nextUsage); })
      .catch((error) => toast.error(error instanceof Error ? error.message : tNow("toast.action_failed")));
    return () => { active = false; };
  }, [usageFilter, token, session?.user.id, view]);

  function navigate(nextView: View) {
    const hash = hashForView(nextView);
    if (window.location.hash !== `#${hash}`) {
      window.location.hash = hash;
    }
    setView(nextView);
    if (nextView !== "userDetail") setUserDetailID(0);
  }

  function openUser(user: AppUser) {
    const hash = hashForUser(user.id);
    if (window.location.hash !== `#${hash}`) window.location.hash = hash;
    setUserDetailID(user.id);
    setView("userDetail");
  }

  useEffect(() => {
    if (session && session.user.role !== "admin" && (view === "users" || view === "userDetail" || view === "providers" || view === "models")) {
      navigate("dashboard");
    }
  }, [session, view]);

  const todayRange = naturalDayRange();
  const usageIsToday = usageFilter.from === todayRange.from && usageFilter.to === todayRange.to;
  const usageSeries = useMemo(
    () => fillUsageSeries(usage?.series ?? [], usageFilter.from, usageFilter.to, usage?.bucket_minutes ?? 1),
    [usage?.series, usage?.bucket_minutes, usageFilter.from, usageFilter.to],
  );

  if (booting) {
    return (
      <main className="login-page">
        <div className="toolbar login-toolbar">
          <ThemeToggle />
          <LocaleToggle />
        </div>
        <div className="login-brand">
          <BrandIcon name="buzz" className="size-8" />
          <div>
            <h1>Buzz</h1>
            <p>{t("common.loading_admin")}</p>
          </div>
        </div>
      </main>
    );
  }

  if (!session || !config || !stats) {
    return (
      <main className="login-page">
        <div className="toolbar login-toolbar">
          <ThemeToggle />
          <LocaleToggle />
        </div>
        <div className="login-shell">
          <div className="login-brand">
            <BrandIcon name="buzz" className="size-10" />
            <div>
              <h1>Buzz</h1>
              <p>{t("app.subtitle")}</p>
            </div>
          </div>
          <Card className="login-card gap-6">
            <CardHeader className="px-6 text-left">
              <CardTitle className="text-lg">{setupRequired ? t("auth.create_admin") : t("auth.login_title")}</CardTitle>
              <p className="text-sm text-muted-foreground">{setupRequired ? t("auth.create_admin_description") : t("auth.login_description")}</p>
            </CardHeader>
            <CardContent className="px-6">
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  void login();
                }}
              >
                <FieldGroup className="gap-7">
                  <Field>
                    <FieldLabel htmlFor="login-username">{t("auth.username")}</FieldLabel>
                    <Input
                      id="login-username"
                      autoFocus
                      autoComplete="username"
                      value={loginForm.username}
                      onChange={(event) => setLoginForm({ ...loginForm, username: event.target.value })}
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="login-password">{t("auth.password")}</FieldLabel>
                    <Input
                      id="login-password"
                      type="password"
                      autoComplete={setupRequired ? "new-password" : "current-password"}
                      value={loginForm.password}
                      onChange={(event) => setLoginForm({ ...loginForm, password: event.target.value })}
                    />
                  </Field>
                  <Field>
                    <Button className="w-full" size="lg" type="submit" disabled={loading}>
                      <KeyRound size={16} /> {setupRequired ? t("auth.create_admin") : t("auth.login")}
                    </Button>
                    {error && <Alert variant="destructive"><AlertDescription>{error}</AlertDescription></Alert>}
                  </Field>
                </FieldGroup>
              </form>
            </CardContent>
          </Card>
          {version && (
            <p className="text-center text-xs text-muted-foreground/60 select-none">
              v{version}
            </p>
          )}
        </div>
      </main>
    );
  }

  const ownActiveKeys = userAPIKeys.filter((key) => key.valid);

  const title = {
    dashboard: t("nav.dashboard"),
    users: t("nav.users"),
    userDetail: t("users.detail"),
    myKeys: t("nav.my_keys"),
    providers: t("nav.providers"),
    models: t("nav.models"),
  }[view];

  return (
    <AdminLayout
      session={session}
      title={title}
      view={view}
      version={version}
      onNavigate={navigate}
      onChangePassword={() => setShowPasswordDialog(true)}
      onLogout={() => void logout()}
    >
        <div className="page-content flex flex-1 flex-col gap-4 p-4">
          {view === "dashboard" && (
            <DashboardPage
              usage={usage}
              quota={quota}
              usageFilter={usageFilter}
              usageIsToday={usageIsToday}
              usageSeries={usageSeries}
              userAPIKeys={userAPIKeys}
              models={models}
              ownActiveKeys={ownActiveKeys}
              onUsageFilterChange={setUsageFilter}
              onResetUsageToToday={() => setUsageFilter((current) => ({ ...current, ...naturalDayRange() }))}
              onSelectUsageRange={(from, to) => setUsageFilter((current) => ({ ...current, from, to }))}
            />
          )}

          {view === "users" && session.user.role === "admin" && (
            <UsersPage users={users} onNewUser={() => setShowUserDialog(true)} onOpenUser={openUser} onResetWeeklyQuotas={resetWeeklyQuotas} />
          )}

          {view === "userDetail" && session.user.role === "admin" && userDetailID > 0 && (
            <UserDetailPage
              token={token}
              userID={userDetailID}
              currentUserID={session.user.id}
              copiedTarget={copiedTarget}
              onBack={() => navigate("users")}
              onCopyText={(value, target) => void copyText(value, target)}
              onUserUpdated={(updated) => setUsers((current) => current.map((user) => user.id === updated.id ? updated : user))}
            />
          )}

          {view === "myKeys" && (
            <MyApiKeysPage
              userAPIKeys={userAPIKeys}
              newUserKey={newUserKey}
              generatedUserKey={generatedUserKey}
              revealedUserKeys={revealedUserKeys}
              copiedTarget={copiedTarget}
              onNewUserKeyChange={setNewUserKey}
              onCreateUserAPIKey={() => void createUserAPIKey()}
              onClearGeneratedUserKey={() => setGeneratedUserKey(null)}
              onCopyText={(value, target) => void copyText(value, target)}
              onCopyUserAPIKey={(key) => void copyUserAPIKey(key)}
              onToggleUserAPIKeyReveal={(key) => void toggleUserAPIKeyReveal(key)}
              onUpdateUserAPIKey={(key, valid) => void updateUserAPIKey(key, valid)}
              onRequestDeleteUserAPIKey={(key) => setConfirmAction({
                title: t("keys.delete_my_key_title"),
                description: t("keys.delete_my_key_body", { name: key.name }),
                confirmLabel: t("common.delete"),
                successLabel: t("keys.deleted"),
                onConfirm: () => deleteUserAPIKey(key),
              })}
            />
          )}

          {view === "providers" && session.user.role === "admin" && (
            <ProvidersPage
              token={token}
              providers={providers}
              providerKeys={providerKeys}
              providerPresets={providerPresets}
              onReload={() => loadProviders()}
              onRequestDeleteProvider={(provider, onConfirm) => setConfirmAction({
                title: t("common.delete"),
                description: t("providers.delete_confirm", { name: provider.name }),
                confirmLabel: t("common.delete"),
                successLabel: t("common.delete"),
                onConfirm,
              })}
              onRequestDeleteProviderKey={(key, onConfirm) => setConfirmAction({
                title: t("common.delete"),
                description: t("provider_keys.delete_confirm", { name: key.name }),
                confirmLabel: t("common.delete"),
                successLabel: t("common.delete"),
                onConfirm,
              })}
            />
          )}

          {view === "models" && session.user.role === "admin" && (
            <ModelsPage
              token={token}
              providers={providers}
              models={models}
              modelPresets={modelPresets}
              modelRoutes={modelRoutes}
              onReload={() => loadModels()}
              onRequestDeleteItem={(onConfirm) => setConfirmAction({
                title: t("common.delete"),
                description: t("models.delete_confirm"),
                confirmLabel: t("common.delete"),
                successLabel: t("common.delete"),
                onConfirm,
              })}
            />
          )}
        </div>
      <Dialog open={showPasswordDialog} onOpenChange={setShowPasswordDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("user.change_password")}</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void changePassword();
            }}
          >
            <FieldGroup className="gap-7 py-4">
              <Field>
                <FieldLabel htmlFor="current-password">{t("user.current_password")}</FieldLabel>
                <Input
                  id="current-password"
                  type="password"
                  value={passwordForm.current_password}
                  onChange={(event) => setPasswordForm({ ...passwordForm, current_password: event.target.value })}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor="new-password">{t("user.new_password")}</FieldLabel>
                <Input
                  id="new-password"
                  type="password"
                  value={passwordForm.new_password}
                  onChange={(event) => setPasswordForm({ ...passwordForm, new_password: event.target.value })}
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => setShowPasswordDialog(false)}>{t("common.cancel")}</Button>
              <Button
                type="submit"
                disabled={!passwordForm.current_password || !passwordForm.new_password}
              >
                {t("common.save")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog open={showUserDialog} onOpenChange={setShowUserDialog}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("users.new_user")}</DialogTitle>
          </DialogHeader>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              await createUser();
              setShowUserDialog(false);
            }}
          >
            <FieldGroup className="gap-7 py-4">
              <Field>
                <FieldLabel htmlFor="new-user-username">{t("auth.username")}</FieldLabel>
                <Input id="new-user-username" value={newUser.username} onChange={(event) => setNewUser({ ...newUser, username: event.target.value })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="new-user-password">{t("auth.password")}</FieldLabel>
                <Input id="new-user-password" type="password" value={newUser.password} onChange={(event) => setNewUser({ ...newUser, password: event.target.value })} />
              </Field>
              <Field>
                <FieldLabel htmlFor="new-user-role">{t("users.role")}</FieldLabel>
                <Select value={newUser.role} onValueChange={(value) => setNewUser({ ...newUser, role: value })}>
                  <SelectTrigger id="new-user-role"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="user">user</SelectItem>
                    <SelectItem value="admin">admin</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => setShowUserDialog(false)}>{t("common.cancel")}</Button>
              <Button
                type="submit"
                disabled={!newUser.username || !newUser.password}
              >
                {t("common.create")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <AlertDialog open={!!confirmAction} onOpenChange={(open) => !open && setConfirmAction(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmAction?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmAction?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={(event) => {
                event.preventDefault();
                void runConfirmAction();
              }}
            >
              {confirmAction?.confirmLabel || t("common.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </AdminLayout>
  );
}
