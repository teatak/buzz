import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import {
  ArrowLeft,
  Brain,
  Check,
  Copy,
  Eye,
  Info,
  Mic,
  Pencil,
  Plus,
  Settings2,
  Trash2,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import { request } from "../api/client";
import { ModelIcon, ModelIconField, ModelPresetIcon } from "../components/model-icon";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Checkbox } from "../components/ui/checkbox";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "../components/ui/tooltip";
import { UpstreamModelPicker } from "../components/upstream-model-picker";
import { Field } from "../components/ui/field";
import { EnabledToggleButton } from "../components/enabled-toggle-button";
import { FormNumberField, FormSelectField, FormStaticField, FormTextareaField, FormTextField, LabelWithTip } from "../components/form-fields";
import { useLocale } from "../i18n/locale";
import { modelDisplayName } from "../lib/model";
import { cn } from "../lib/utils";
import type { Model, ModelPreset, ModelRoute, ProviderRecord, UpstreamModel } from "../types/admin";

type ModelsPageProps = {
  token: string;
  providers: ProviderRecord[];
  models: Model[];
  modelPresets: ModelPreset[];
  modelRoutes: ModelRoute[];
  onReload: () => Promise<void>;
  onRequestDeleteItem: (onConfirm: () => Promise<void>) => void;
};

const defaultCapabilities = JSON.stringify({
  tools: false,
  vision: false,
  reasoning: false,
  audio_input: false,
}, null, 2);

const modelDefaults: Model = {
  id: 0,
  name: "",
  display_name: "",
  icon: "",
  description: "",
  context_window: 0,
  max_input_tokens: 0,
  max_output_tokens: 0,
  quota_uncached_input_rate: 1,
  quota_cached_input_rate: 1,
  quota_output_rate: 1,
  capabilities: defaultCapabilities,
  selection_policy: "round_robin",
  enabled: true,
};

const capabilityOptions = [
  "tools",
  "vision",
  "reasoning",
  "audio_input",
] as const;

const routeDefaults = {
  id: 0,
  model_id: 0,
  provider_id: 0,
  upstream_protocol: "auto",
  upstream_model: "",
  enabled: true,
  priority: 0,
  weight: 1,
};

export function ModelsPage(props: ModelsPageProps) {
  const { t, locale } = useLocale();
  const upstreamModelID = useId();
  const [modelOpen, setModelOpen] = useState(false);
  const [presetOpen, setPresetOpen] = useState(false);
  const [routeOpen, setRouteOpen] = useState(false);
  const [modelForm, setModelForm] = useState<Model>(modelDefaults);
  const [presetIDs, setPresetIDs] = useState<string[]>([]);
  const [anchorIndex, setAnchorIndex] = useState<number | null>(null);
  const [lastShiftRange, setLastShiftRange] = useState<string[]>([]);
  const [routeForm, setRouteForm] = useState(routeDefaults);
  const [selectedUpstream, setSelectedUpstream] = useState<UpstreamModel | null>(null);
  const [syncMetadata, setSyncMetadata] = useState(false);
  const [saving, setSaving] = useState(false);
  const [selectedModelID, setSelectedModelID] = useState<number | null>(null);
  const [copiedTarget, setCopiedTarget] = useState("");
  const copiedTimer = useRef<number | null>(null);

  const providerName = useMemo(() => new Map(props.providers.map((provider) => [provider.id, provider.name])), [props.providers]);
  const existingModelNames = useMemo(() => new Set(props.models.map((model) => model.name)), [props.models]);
  const routesByModel = useMemo(() => {
    const routes = new Map<number, ModelRoute[]>();
    for (const route of props.modelRoutes) {
      routes.set(route.model_id, [...(routes.get(route.model_id) ?? []), route]);
    }
    return routes;
  }, [props.modelRoutes]);
  const selectedPresets = props.modelPresets.filter((preset) => presetIDs.includes(preset.id));
  const selectedPresetCount = selectedPresets.length;
  const hasUpstreamMetadata = Boolean(
    selectedUpstream &&
      (selectedUpstream.context_window ||
        selectedUpstream.max_output_tokens ||
        selectedUpstream.max_input_tokens ||
        selectedUpstream.quota_uncached_input_rate !== undefined ||
        selectedUpstream.quota_cached_input_rate !== undefined ||
        selectedUpstream.quota_output_rate !== undefined ||
        Object.keys(selectedUpstream.capabilities ?? {}).length),
  );
  const selectedRouteModel = props.models.find((model) => model.id === routeForm.model_id);
  const selectedRouteProvider = props.providers.find((provider) => provider.id === routeForm.provider_id);
  const selectedRouteEndpoints = selectedRouteProvider?.endpoints ?? [];
  const selectedModel = selectedModelID == null ? null : props.models.find((model) => model.id === selectedModelID) ?? null;
  const selectedModelRoutes = selectedModel ? routesByModel.get(selectedModel.id) ?? [] : [];

  useEffect(() => () => {
    if (copiedTimer.current) window.clearTimeout(copiedTimer.current);
  }, []);

  async function save(path: string, body: unknown, method = "POST") {
    setSaving(true);
    try {
      await request(path, props.token, { method, body: JSON.stringify(body) });
      try {
        await props.onReload();
        toast.success(t("common.save"));
      } catch {
        // The write succeeded: finish the form even if refreshing the list fails.
        toast.warning(t("toast.saved_refresh_failed"));
      }
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("toast.action_failed"));
      return false;
    } finally {
      setSaving(false);
    }
  }

  async function remove(path: string) {
    setSaving(true);
    try {
      await request(path, props.token, { method: "DELETE" });
      await props.onReload();
    } finally {
      setSaving(false);
    }
  }

  async function copyModelName(name: string, target: string) {
    try {
      if (window.navigator.clipboard?.writeText && window.isSecureContext) {
        await window.navigator.clipboard.writeText(name);
      } else {
        fallbackCopyText(name);
      }
      setCopiedTarget(target);
      if (copiedTimer.current) window.clearTimeout(copiedTimer.current);
      copiedTimer.current = window.setTimeout(() => setCopiedTarget(""), 1200);
    } catch {
      toast.error(t("common.copy_failed"));
    }
  }

  function openModel(model?: Model) {
    setModelForm(model ? { ...model, capabilities: model.capabilities || defaultCapabilities } : modelDefaults);
    setModelOpen(true);
  }

  function openRoute(modelID: number, route?: ModelRoute) {
    setSelectedUpstream(null);
    setSyncMetadata((routesByModel.get(modelID)?.length ?? 0) === 0);
    const provider = props.providers[0];
    const providerID = provider?.id ?? 0;
    const model = props.models.find((item) => item.id === modelID);
    const upstreamModel = model?.name ?? "";
    setRouteForm(route ? { ...route } : {
      ...routeDefaults,
      model_id: modelID,
      provider_id: providerID,
      upstream_model: upstreamModel,
    });
    setRouteOpen(true);
  }

  async function submitModel() {
    await save("/admin/api/models", modelForm, modelForm.id ? "PUT" : "POST");
    setModelOpen(false);
  }

  async function submitPreset() {
    if (presetIDs.length === 0) return;
    await save("/admin/api/model-presets", { ids: presetIDs });
    setPresetOpen(false);
    setPresetIDs([]);
    setAnchorIndex(null);
    setLastShiftRange([]);
  }

  function togglePreset(preset: ModelPreset, index: number, shiftKey: boolean) {
    if (existingModelNames.has(preset.name)) return;

    if (shiftKey && anchorIndex !== null) {
      const [start, end] = [anchorIndex, index].sort((a, b) => a - b);
      const newRangeIDs = props.modelPresets
        .slice(start, end + 1)
        .filter((item) => !existingModelNames.has(item.name))
        .map((item) => item.id);

      setPresetIDs((current) => {
        const next = current.filter((id) => !(lastShiftRange.includes(id) && !newRangeIDs.includes(id)));
        newRangeIDs.forEach((id) => {
          if (!next.includes(id)) {
            next.push(id);
          }
        });
        return next;
      });
      setLastShiftRange(newRangeIDs);
    } else {
      setPresetIDs((current) => {
        return current.includes(preset.id) ? current.filter((id) => id !== preset.id) : [...current, preset.id];
      });
      setAnchorIndex(index);
      setLastShiftRange([]);
    }
  }

  async function submitRoute() {
    const model_metadata = syncMetadata && hasUpstreamMetadata && selectedUpstream ? {
      context_window: selectedUpstream.context_window,
      max_input_tokens: selectedUpstream.max_input_tokens,
      max_output_tokens: selectedUpstream.max_output_tokens,
      quota_uncached_input_rate: selectedUpstream.quota_uncached_input_rate,
      quota_cached_input_rate: selectedUpstream.quota_cached_input_rate,
      quota_output_rate: selectedUpstream.quota_output_rate,
      capabilities: selectedUpstream.capabilities,
    } : undefined;
    if (await save("/admin/api/model-routes", { ...routeForm, model_metadata }, routeForm.id ? "PUT" : "POST")) {
      setRouteOpen(false);
    }
  }

  return (
    <div className="stack">
      {selectedModel ? (
        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex min-w-0 items-start gap-3">
                <Button type="button" variant="ghost" size="icon" onClick={() => setSelectedModelID(null)} aria-label={t("models.back_to_models")}>
                  <ArrowLeft />
                </Button>
                <ModelIcon model={selectedModel} presets={props.modelPresets} className="mt-0.5 h-10 w-10" />
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <CardTitle>{modelDisplayName(selectedModel)}</CardTitle>
                    <ModelMultiplierBadge model={selectedModel} />
                    <ModelDescriptionInfo description={selectedModel.description} />
                    <StatusBadge enabled={selectedModel.enabled} />
                  </div>
                  <div className="mt-1 flex min-w-0 items-center gap-1.5">
                    <div className="truncate mono text-sm text-muted-foreground">{selectedModel.name}</div>
                    <CopyModelNameButton
                      copied={copiedTarget === `model-name-${selectedModel.id}`}
                      onCopy={() => copyModelName(selectedModel.name, `model-name-${selectedModel.id}`)}
                    />
                  </div>
                  <ModelCapabilityIcons capabilities={selectedModel.capabilities} className="mt-2" />
                </div>
              </div>
              <div className="flex items-center gap-1">
                <Button type="button" variant="outline" onClick={() => openRoute(selectedModel.id)}><Plus />{t("models.new_route")}</Button>
                <RowActions
                  onEdit={() => openModel(selectedModel)}
                  onToggle={() => void save("/admin/api/models", { ...selectedModel, enabled: !selectedModel.enabled }, "PUT")}
                  onDelete={() => props.onRequestDeleteItem(() => remove(`/admin/api/models?id=${selectedModel.id}`))}
                  enabled={selectedModel.enabled}
                />
              </div>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              <ModelStat label={t("models.context_window")} value={formatModelNumber(selectedModel.context_window)} />
              <ModelStat label={t("models.max_output_tokens")} value={formatModelNumber(selectedModel.max_output_tokens)} />
              <ModelStat label={t("models.policy")} value={policyLabel(t, selectedModel.selection_policy)} />
            </div>
            <div className="space-y-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <h3 className="text-sm font-medium">{t("models.quota_rates")}</h3>
                  <ModelMultiplierBadge model={selectedModel} />
                </div>
                <span className="text-xs text-muted-foreground">{t("models.quota_rates_tip")}</span>
              </div>
              <div className="grid gap-3 sm:grid-cols-3">
                <ModelStat label={t("models.quota_cached_input_rate")} value={formatQuotaRateWithUSD(selectedModel.quota_cached_input_rate, locale)} mono />
                <ModelStat label={t("models.quota_uncached_input_rate")} value={formatQuotaRateWithUSD(selectedModel.quota_uncached_input_rate, locale)} mono />
                <ModelStat label={t("models.quota_output_rate")} value={formatQuotaRateWithUSD(selectedModel.quota_output_rate, locale)} mono />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <h3 className="text-lg font-semibold">{t("models.routes")}</h3>
              <Badge variant="secondary">{selectedModelRoutes.length}</Badge>
            </div>
            {selectedModelRoutes.length === 0 ? (
              <div className="rounded-md border border-dashed p-4 text-sm text-muted-foreground">{t("models.no_routes")}</div>
            ) : (
              <div className="data-table-card">
                <Table className="keys-table-inner">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("models.provider")}</TableHead>
                      <TableHead>{t("models.upstream_protocol")}</TableHead>
                      <TableHead>{t("models.upstream_model")}</TableHead>
                      <TableHead className="right">{t("models.priority")}</TableHead>
                      <TableHead className="right">{t("models.weight")}</TableHead>
                      <TableHead>{t("common.status")}</TableHead>
                      <TableHead className="right">{t("common.actions")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {selectedModelRoutes.map((route) => (
                      <TableRow key={route.id}>
                        <TableCell className="mono">{route.provider_name ?? providerName.get(route.provider_id) ?? route.provider_id}</TableCell>
                        <TableCell className="mono">{route.upstream_protocol === "auto" ? t("models.protocol_auto") : providerProtocolLabel(route.upstream_protocol)}</TableCell>
                        <TableCell className="mono">{route.upstream_model}</TableCell>
                        <TableCell className="right mono">{route.priority}</TableCell>
                        <TableCell className="right mono">{route.weight}</TableCell>
                        <TableCell><StatusBadge enabled={route.enabled} /></TableCell>
                        <TableCell className="right">
                          <RowActions
                            onEdit={() => openRoute(selectedModel.id, route)}
                            onToggle={() => void save("/admin/api/model-routes", { ...route, enabled: !route.enabled }, "PUT")}
                            onDelete={() => props.onRequestDeleteItem(() => remove(`/admin/api/model-routes?id=${route.id}`))}
                            enabled={route.enabled}
                          />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </CardContent>
        </Card>
      ) : (
        <div className="stack">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <h2 className="text-2xl font-semibold tracking-tight">{t("models.public_models")}</h2>
              <Badge variant="secondary">{props.models.length}</Badge>
            </div>
            <div className="flex gap-2">
              <Button type="button" variant="outline" onClick={() => {
                setPresetIDs([]);
                setAnchorIndex(null);
                setLastShiftRange([]);
                setPresetOpen(true);
              }}>
                <Plus />{t("models.add_from_preset")}
              </Button>
              <Button type="button" onClick={() => openModel()}><Plus />{t("models.new_model")}</Button>
            </div>
          </div>

          <div className="grid items-start gap-3 xl:grid-cols-2 2xl:grid-cols-3">
            {props.models.map((model) => {
              const routes = routesByModel.get(model.id) ?? [];
              return (
                <Card
                  key={model.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => setSelectedModelID(model.id)}
                  onKeyDown={(event) => activateCard(event, () => setSelectedModelID(model.id))}
                  className="self-start cursor-pointer transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
                >
                  <CardContent className="px-4">
                    <div className="grid gap-3">
                      <div className="flex items-start justify-between gap-3">
                        <div className="flex min-w-0 items-start gap-3">
                          <ModelIcon model={model} presets={props.modelPresets} />
                          <div className="min-w-0">
                            <div className="flex min-w-0 items-center gap-2">
                              <div className="truncate text-base font-semibold">{modelDisplayName(model)}</div>
                              <ModelMultiplierBadge model={model} />
                              <ModelDescriptionInfo description={model.description} />
                            </div>
                            <div className="mt-0.5 flex min-w-0 items-center gap-2">
                              <div className="truncate mono text-xs text-muted-foreground">{model.name}</div>
                              <CopyModelNameButton
                                copied={copiedTarget === `model-name-${model.id}`}
                                onCopy={() => copyModelName(model.name, `model-name-${model.id}`)}
                              />
                              <ModelValueChip value={formatModelNumber(model.context_window)} />
                            </div>
                            <ModelCapabilityIcons capabilities={model.capabilities} className="mt-2" />
                          </div>
                        </div>
                        <StatusBadge enabled={model.enabled} />
                      </div>
                      <div className="flex items-center justify-between gap-3 border-t pt-3">
                        <ModelRouteChip label={t("models.routes")} count={routes.length} />
                        <div className="flex justify-end" onClick={stopCardAction}>
                          <RowActions
                            onEdit={() => openModel(model)}
                            onToggle={() => void save("/admin/api/models", { ...model, enabled: !model.enabled }, "PUT")}
                            onDelete={() => props.onRequestDeleteItem(() => remove(`/admin/api/models?id=${model.id}`))}
                            enabled={model.enabled}
                          />
                        </div>
                      </div>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
          </div>
        </div>
      )}

      <Dialog open={modelOpen} onOpenChange={setModelOpen}>
        <DialogContent className="max-h-[min(880px,calc(100dvh-2rem))] gap-3 overflow-y-auto sm:max-w-2xl">
          <DialogHeader><DialogTitle>{modelForm.id ? t("models.edit_model") : t("models.new_model")}</DialogTitle></DialogHeader>
          <div className="grid gap-4 py-3">
            <div className="grid gap-4 sm:grid-cols-2">
              <FormTextField label={t("model.model")} value={modelForm.name} onChange={(name) => setModelForm({ ...modelForm, name })} />
              <FormTextField label={t("models.display_name")} value={modelForm.display_name} onChange={(display_name) => setModelForm({ ...modelForm, display_name })} />
            </div>
            <ModelIconField
              model={modelForm}
              presets={props.modelPresets}
              onChange={(icon) => setModelForm({ ...modelForm, icon })}
            />
            <FormTextareaField label={t("models.description")} className="min-h-16" value={modelForm.description} onChange={(description) => setModelForm({ ...modelForm, description })} />
            <div className="grid gap-4 sm:grid-cols-2">
              <TokenNumberField label={t("models.context_window")} value={modelForm.context_window} onChange={(context_window) => setModelForm({ ...modelForm, context_window })} />
              <TokenNumberField label={t("models.max_output_tokens")} value={modelForm.max_output_tokens} onChange={(max_output_tokens) => setModelForm({ ...modelForm, max_output_tokens })} />
            </div>
            <div className="space-y-3 rounded-lg border p-3">
              <div className="flex flex-col gap-1">
                <div className="text-sm font-medium">{t("models.quota_rates")}</div>
                <div className="text-xs text-muted-foreground">{t("models.quota_rates_tip")}</div>
              </div>
              <div className="grid gap-4 sm:grid-cols-3">
                <FormNumberField label={t("models.quota_cached_input_rate")} min={0} step="any" value={modelForm.quota_cached_input_rate} onChange={(quota_cached_input_rate) => setModelForm({ ...modelForm, quota_cached_input_rate })} />
                <FormNumberField label={t("models.quota_uncached_input_rate")} min={0} step="any" value={modelForm.quota_uncached_input_rate} onChange={(quota_uncached_input_rate) => setModelForm({ ...modelForm, quota_uncached_input_rate })} />
                <FormNumberField label={t("models.quota_output_rate")} min={0} step="any" value={modelForm.quota_output_rate} onChange={(quota_output_rate) => setModelForm({ ...modelForm, quota_output_rate })} />
              </div>
              {(() => {
                const info = calculateModelMultiplier(modelForm);
                if (!info) return null;
                const displayMultiplier = info.isFree ? t("models.cost_free") : info.multiplier;
                return (
                  <div className="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2 text-xs">
                    <span className="text-muted-foreground">{t("models.estimated_cost_multiplier")}</span>
                    <div className="flex items-center gap-2">
                      <Badge
                        variant="secondary"
                        className={cn(
                          "h-5 shrink-0 rounded px-1.5 mono text-[11px] font-medium",
                          info.isFree ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 font-semibold" : "text-muted-foreground",
                        )}
                      >
                        {displayMultiplier}
                      </Badge>
                      <span className="mono text-muted-foreground">
                        {t("models.cost_multiplier_tip", {
                          multiplier: displayMultiplier,
                          cost: info.cost === 0 ? "0" : info.cost.toFixed(1),
                        })}
                      </span>
                    </div>
                  </div>
                );
              })()}
            </div>
            <CapabilityField value={modelForm.capabilities} onChange={(capabilities) => setModelForm({ ...modelForm, capabilities })} />
            <div className="grid gap-4 sm:grid-cols-2">
              <FormSelectField
                label={t("models.policy")}
                tip={t("models.tip_policy")}
                value={modelForm.selection_policy}
                options={["round_robin", "weighted"].map((value) => ({ value, label: policyLabel(t, value) }))}
                onChange={(selection_policy) => setModelForm({ ...modelForm, selection_policy })}
              />
              <EnabledSelect value={modelForm.enabled} onChange={(enabled) => setModelForm({ ...modelForm, enabled })} />
            </div>
          </div>
          <DialogFooter>
            <Button disabled={saving} onClick={() => void submitModel()}>{t("common.save")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={presetOpen} onOpenChange={(open) => {
        setPresetOpen(open);
        if (!open) {
          setPresetIDs([]);
          setAnchorIndex(null);
          setLastShiftRange([]);
        }
      }}>
        <DialogContent className="sm:max-w-4xl">
          <DialogHeader><DialogTitle>{t("models.add_from_preset")}</DialogTitle></DialogHeader>
          <div className="grid gap-4 py-4">
            <div className="grid max-h-[min(600px,65dvh)] gap-2 overflow-y-auto pr-1 sm:grid-cols-3 lg:grid-cols-4">
              {props.modelPresets.map((preset, index) => {
                const exists = existingModelNames.has(preset.name);
                const selected = presetIDs.includes(preset.id);
                return (
                  <button
                    key={preset.id}
                    type="button"
                    disabled={exists}
                    onClick={(event) => togglePreset(preset, index, event.shiftKey)}
                    className={`flex min-w-0 items-center gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors enabled:hover:bg-muted/50 disabled:cursor-not-allowed disabled:opacity-50 ${selected ? "border-primary bg-primary/5" : "border-border bg-background"}`}
                  >
                    <ModelPresetIcon preset={preset} className="h-6 w-6 rounded-[6px]" />
                    <span className="truncate text-sm font-medium flex-1">{preset.display_name}</span>
                    {exists && <Badge variant="outline" className="px-1.5 py-0 text-[10px] h-5 leading-[18px] font-normal">{t("models.already_exists")}</Badge>}
                  </button>
                );
              })}
            </div>
            <div className="h-28">
              {selectedPresetCount === 1 && selectedPresets[0] && (
                <div className="flex h-full min-w-0 flex-col justify-between overflow-hidden rounded-md border p-3 text-sm">
                  <div className="flex min-w-0 items-center gap-3">
                    <ModelPresetIcon preset={selectedPresets[0]} />
                    <div className="min-w-0 flex-1">
                      <div className="flex min-w-0 items-center gap-2">
                        <strong className="truncate">{selectedPresets[0].display_name}</strong>
                        <Badge variant="secondary">{selectedPresets[0].family}</Badge>
                        <span className="truncate mono text-xs text-muted-foreground">{selectedPresets[0].name}</span>
                      </div>
                      <div className="mt-1 truncate text-muted-foreground">{selectedPresets[0].description}</div>
                    </div>
                  </div>
                  <div className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-2">
                    <div><span>{t("models.context_window")}: </span><span className="mono">{formatModelNumber(selectedPresets[0].context_window)}</span></div>
                    <div><span>{t("models.max_output_tokens")}: </span><span className="mono">{formatModelNumber(selectedPresets[0].max_output_tokens)}</span></div>
                  </div>
                </div>
              )}
              {selectedPresetCount > 1 && (
                <div className="flex h-full items-center rounded-md border bg-muted/40 p-4 text-sm font-medium">
                  {t("models.selected_models", { count: selectedPresetCount })}
                </div>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button disabled={saving || selectedPresetCount === 0} onClick={() => void submitPreset()}>
              {selectedPresetCount > 0 ? `${t("models.add_model")} ${selectedPresetCount}` : t("models.add_model")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={routeOpen} onOpenChange={setRouteOpen}>
        <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-lg">
          <DialogHeader><DialogTitle>{routeForm.id ? t("models.edit_route") : t("models.new_route")}</DialogTitle></DialogHeader>
          <div className="grid min-h-0 gap-4 overflow-y-auto py-2">
            <FormStaticField label={t("model.model")}>
              <div className="rounded-md border bg-muted px-3 py-2 mono text-sm">{selectedRouteModel?.name ?? routeForm.model_id}</div>
            </FormStaticField>
            <FormSelectField
              label={t("models.provider")}
              tip={t("models.tip_provider")}
              value={String(routeForm.provider_id)}
              options={props.providers.map((provider) => ({ value: String(provider.id), label: provider.name }))}
              onChange={(value) => {
                const provider_id = Number(value);
                setSelectedUpstream(null);
                setRouteForm({ ...routeForm, provider_id, upstream_protocol: "auto" });
              }}
            />
            <FormSelectField
              label={t("models.upstream_protocol")}
              tip={t("models.tip_upstream_protocol")}
              value={routeForm.upstream_protocol}
              options={[
                { value: "auto", label: t("models.protocol_auto") },
                ...selectedRouteEndpoints.map((endpoint) => ({
                  value: endpoint.protocol,
                  label: `${providerProtocolLabel(endpoint.protocol)}${endpoint.enabled ? "" : ` · ${t("common.disabled")}`}`,
                })),
              ]}
              onChange={(value) => { setSelectedUpstream(null); setRouteForm({ ...routeForm, upstream_protocol: value }); }}
            />
            <Field>
              <LabelWithTip htmlFor={upstreamModelID} label={t("models.upstream_model")} tip={t("models.tip_upstream_model")} />
              <div className="flex items-center gap-2">
                <Input
                  id={upstreamModelID}
                  value={routeForm.upstream_model}
                  onChange={(e) => { setSelectedUpstream(null); setRouteForm({ ...routeForm, upstream_model: e.target.value }); }}
                />
                <UpstreamModelPicker
                  providerId={routeForm.provider_id}
                  protocol={routeForm.upstream_protocol}
                  token={props.token}
                  onSelect={(model) => { setSelectedUpstream(model); setRouteForm({ ...routeForm, upstream_model: model.id }); }}
                />
              </div>
            </Field>
            {selectedUpstream && hasUpstreamMetadata ? (
              <div className="space-y-3 rounded-lg border bg-muted/30 p-3">
                <label className="flex items-center gap-2 text-sm font-medium">
                  <Checkbox checked={syncMetadata} onCheckedChange={(checked) => setSyncMetadata(checked === true)} />
                  {t("models.sync_metadata")}
                </label>
                <p className="text-xs text-muted-foreground">{t("models.sync_metadata_hint")}</p>
                <div className="grid grid-cols-2 gap-3 text-sm">
                  {!!selectedUpstream.context_window && <ModelStat label={t("models.context_window")} value={formatModelNumber(selectedUpstream.context_window)} />}
                  {!!selectedUpstream.max_output_tokens && <ModelStat label={t("models.max_output_tokens")} value={formatModelNumber(selectedUpstream.max_output_tokens)} />}
                  {!!selectedUpstream.max_input_tokens && <ModelStat label={t("models.max_input_tokens")} value={formatModelNumber(selectedUpstream.max_input_tokens)} />}
                  {selectedUpstream.quota_uncached_input_rate !== undefined && (
                    <ModelStat label={t("models.quota_uncached_input_rate")} value={formatQuotaRateWithUSD(selectedUpstream.quota_uncached_input_rate, locale)} mono />
                  )}
                  {selectedUpstream.quota_cached_input_rate !== undefined && (
                    <ModelStat label={t("models.quota_cached_input_rate")} value={formatQuotaRateWithUSD(selectedUpstream.quota_cached_input_rate, locale)} mono />
                  )}
                  {selectedUpstream.quota_output_rate !== undefined && (
                    <ModelStat label={t("models.quota_output_rate")} value={formatQuotaRateWithUSD(selectedUpstream.quota_output_rate, locale)} mono />
                  )}
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {capabilityOptions.filter((key) => selectedUpstream.capabilities?.[key] !== undefined).map((key) => (
                    <Badge key={key} variant="outline" className="font-normal">
                      {t(`models.capability_${key}`)} · {t(selectedUpstream.capabilities![key] ? "models.supported" : "models.unsupported")}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : selectedUpstream ? <p className="text-xs text-muted-foreground">{t("models.no_metadata")}</p> : null}
            <div className="grid grid-cols-3 items-start gap-3">
              <FormNumberField label={t("models.priority")} tip={t("models.tip_priority")} value={routeForm.priority} onChange={(priority) => setRouteForm({ ...routeForm, priority })} />
              <FormNumberField label={t("models.weight")} tip={t("models.tip_weight")} value={routeForm.weight} onChange={(weight) => setRouteForm({ ...routeForm, weight })} />
              <EnabledSelect value={routeForm.enabled} onChange={(enabled) => setRouteForm({ ...routeForm, enabled })} />
            </div>
          </div>
          <DialogFooter><Button disabled={saving || !routeForm.model_id || !routeForm.provider_id || !routeForm.upstream_model} onClick={() => void submitRoute()}>{t("common.save")}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function stopCardAction(event: MouseEvent<HTMLDivElement>) {
  event.stopPropagation();
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

function activateCard(event: KeyboardEvent<HTMLElement>, onActivate: () => void) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  onActivate();
}

function policyLabel(t: (key: string) => string, policy: string) {
  if (policy === "weighted") return t("models.policy_weighted");
  if (policy === "round_robin") return t("models.policy_round_robin");
  return policy;
}

function ModelStat({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0 rounded-lg bg-muted/50 px-3 py-2">
      <div className="truncate text-xs text-muted-foreground">{label}</div>
      <div className={`mt-1 truncate text-sm font-medium ${mono ? "mono" : ""}`}>{value || "-"}</div>
    </div>
  );
}

function ModelChip({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <Badge variant="secondary" className="h-6 shrink-0 gap-1 rounded-full px-2 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? "mono" : ""}>{value || "-"}</span>
    </Badge>
  );
}

function ModelRouteChip({ label, count }: { label: string; count: number }) {
  if (count === 0) {
    return (
      <Badge variant="outline" className="h-6 shrink-0 gap-1 rounded-full border-dashed px-2 text-xs text-muted-foreground">
        <span>{label}</span>
        <span>0</span>
      </Badge>
    );
  }
  return <ModelChip label={label} value={String(count)} />;
}

function ModelValueChip({ value }: { value: string }) {
  return <Badge variant="secondary" className="h-5 shrink-0 rounded-full px-2 mono text-[11px]">{value || "-"}</Badge>;
}

export function calculateModelMultiplier(model: {
  quota_cached_input_rate?: number;
  quota_uncached_input_rate?: number;
  quota_output_rate?: number;
}): { isFree: boolean; multiplier: string; cost: number } | null {
  const cached = Number(model.quota_cached_input_rate) || 0;
  const uncached = Number(model.quota_uncached_input_rate) || 0;
  const output = Number(model.quota_output_rate) || 0;
  // 综合成本 = 缓存输入*0.64 + 未缓存输入*0.16 + 输出*0.20 (按 80% 缓存命中率基准)
  const cost = cached * 0.64 + uncached * 0.16 + output * 0.20;
  if (cost < 0 || isNaN(cost)) return null;
  if (cost === 0) {
    return { isFree: true, multiplier: "FREE", cost: 0 };
  }
  const multiplier = cost / 1000;
  let formatted: string;
  if (multiplier < 0.01) {
    formatted = "<0.01x";
  } else if (multiplier < 1) {
    formatted = `${parseFloat(multiplier.toFixed(2))}x`;
  } else {
    formatted = `${parseFloat(multiplier.toFixed(1))}x`;
  }
  return { isFree: false, multiplier: formatted, cost };
}

function ModelMultiplierBadge({
  model,
  className,
}: {
  model: Pick<Model, "quota_cached_input_rate" | "quota_uncached_input_rate" | "quota_output_rate">;
  className?: string;
}) {
  const { t } = useLocale();
  const info = calculateModelMultiplier(model);
  if (!info) return null;

  const displayMultiplier = info.isFree ? t("models.cost_free") : info.multiplier;

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge
          variant="secondary"
          className={cn(
            "h-5 shrink-0 rounded px-1.5 mono text-[11px] font-medium cursor-default select-none",
            info.isFree ? "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 font-semibold" : "text-muted-foreground hover:text-foreground",
            className,
          )}
        >
          {displayMultiplier}
        </Badge>
      </TooltipTrigger>
      <TooltipContent>
        {t("models.cost_multiplier_tip", {
          multiplier: displayMultiplier,
          cost: info.cost === 0 ? "0" : info.cost.toFixed(1),
        })}
      </TooltipContent>
    </Tooltip>
  );
}

function CopyModelNameButton({ copied, onCopy }: { copied: boolean; onCopy: () => void }) {
  const { t } = useLocale();
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground"
          onClick={(event) => {
            event.stopPropagation();
            onCopy();
          }}
          aria-label={t("common.copy")}
        >
          {copied
            ? <Check className="text-emerald-600 dark:text-emerald-400" size={15} />
            : <Copy size={15} />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{copied ? t("common.copied") : t("common.copy")}</TooltipContent>
    </Tooltip>
  );
}

const capabilityIconMap: Record<(typeof capabilityOptions)[number], LucideIcon> = {
  tools: Wrench,
  vision: Eye,
  reasoning: Brain,
  audio_input: Mic,
};

function ModelDescriptionInfo({ description }: { description?: string }) {
  const { t } = useLocale();
  const text = description?.trim();
  if (!text) return null;

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label={t("models.description")}
        >
          <Info className="h-3.5 w-3.5" strokeWidth={2.2} />
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-80 whitespace-normal text-left leading-relaxed">{text}</TooltipContent>
    </Tooltip>
  );
}

function ModelCapabilityIcons({ capabilities, className = "" }: { capabilities: string; className?: string }) {
  const { t } = useLocale();
  const parsed = parseCapabilities(capabilities);
  const enabledCapabilities = capabilityOptions.filter((name) => parsed[name]);
  if (enabledCapabilities.length === 0) return null;

  return (
    <div className={`flex flex-wrap items-center gap-1 ${className}`}>
      {enabledCapabilities.map((name) => {
        const Icon = capabilityIconMap[name];
        const label = t(`models.capability_${name}`);
        return (
          <Tooltip key={name}>
            <TooltipTrigger asChild>
              <span
                className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-lg bg-muted/70 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                aria-label={label}
              >
                <Icon className="h-3.5 w-3.5" strokeWidth={2} />
              </span>
            </TooltipTrigger>
            <TooltipContent>{label}</TooltipContent>
          </Tooltip>
        );
      })}
    </div>
  );
}

function formatModelNumber(value: number) {
  if (!value) return "-";
  if (value >= 1_000_000) return `${Number((value / 1_000_000).toFixed(2))}M`;
  if (value >= 1_000) return `${Number((value / 1_000).toFixed(1))}K`;
  return String(value);
}

function formatQuotaRate(value: number | undefined | null, locale: string) {
  if (value == null || isNaN(value)) return "-";
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(value);
}

function formatQuotaRateWithUSD(value: number | undefined | null, locale: string) {
  if (value == null || isNaN(value)) return "-";
  const formatted = new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(value);
  if (value === 0) return `${formatted} ($0)`;
  const usd = value / 1000;
  let usdStr: string;
  if (usd >= 1) {
    usdStr = `$${new Intl.NumberFormat(locale, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(usd)}`;
  } else {
    usdStr = `$${parseFloat(usd.toFixed(4))}`;
  }
  return `${formatted} (~${usdStr}/M)`;
}

function RowActions(props: { onEdit: () => void; onToggle: () => void; onDelete: () => void; enabled: boolean }) {
  const { t } = useLocale();
  return (
    <div className="flex justify-end gap-1">
      <Button type="button" variant="ghost" size="icon-sm" onClick={props.onEdit} aria-label={t("common.edit")}><Pencil /></Button>
      <EnabledToggleButton enabled={props.enabled} size="icon-sm" onClick={props.onToggle} />
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="text-destructive hover:bg-destructive/10 hover:text-destructive"
        onClick={props.onDelete}
        aria-label={t("common.delete")}
      >
        <Trash2 />
      </Button>
    </div>
  );
}

function EnabledSelect(props: { value: boolean; onChange: (value: boolean) => void }) {
  const { t } = useLocale();
  return (
    <FormSelectField
      label={t("common.status")}
      value={props.value ? "1" : "0"}
      options={[
        { value: "1", label: t("common.active") },
        { value: "0", label: t("common.disabled") },
      ]}
      onChange={(value) => props.onChange(value === "1")}
    />
  );
}

function TokenNumberField(props: { label: string; value: number; onChange: (value: number) => void }) {
  const id = useId();
  const [draft, setDraft] = useState(formatTokenDraft(props.value));

  useEffect(() => {
    setDraft(formatTokenDraft(props.value));
  }, [props.value]);

  function commit() {
    const parsed = parseTokenNumber(draft);
    if (parsed == null) {
      setDraft(formatTokenDraft(props.value));
      return;
    }
    props.onChange(parsed);
    setDraft(formatTokenDraft(parsed));
  }

  return (
    <Field>
      <LabelWithTip htmlFor={id} label={props.label} />
      <div className="flex min-w-0 items-center gap-2">
        <Input
          id={id}
          className="min-w-0 flex-1"
          inputMode="numeric"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.currentTarget.blur();
            }
          }}
        />
        <span className="shrink-0 text-sm tabular-nums text-muted-foreground">
          {formatTokenHint(parseTokenNumber(draft) ?? props.value)}
        </span>
      </div>
    </Field>
  );
}

function CapabilityField(props: { value: string; onChange: (value: string) => void }) {
  const { t } = useLocale();
  const idPrefix = useId();
  const capabilities = parseCapabilities(props.value);

  function setCapability(name: string, enabled: boolean) {
    props.onChange(JSON.stringify({ ...capabilities, [name]: enabled }, null, 2));
  }

  return (
    <FormStaticField label={t("models.capabilities")}>
      <div className="grid grid-cols-2 gap-2 rounded-md border bg-muted/20 p-2 sm:grid-cols-4">
        {capabilityOptions.map((name) => {
          const id = `${idPrefix}-${name}`;
          return (
            <label key={name} htmlFor={id} className="flex items-center gap-3 rounded-md px-2 py-1.5 text-sm hover:bg-muted">
              <Checkbox id={id} checked={Boolean(capabilities[name])} onCheckedChange={(checked) => setCapability(name, checked === true)} />
              <span>{t(`models.capability_${name}`)}</span>
            </label>
          );
        })}
      </div>
    </FormStaticField>
  );
}

function parseCapabilities(value: string): Record<string, boolean> {
  try {
    const parsed = JSON.parse(value);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return Object.fromEntries(Object.entries(parsed).map(([key, enabled]) => [key, Boolean(enabled)]));
  } catch {
    return {};
  }
}

function parseTokenNumber(value: string) {
  const normalized = value.trim().replace(/,/g, "");
  if (!normalized) return 0;
  if (!/^\d+$/.test(normalized)) return null;
  const amount = Number(normalized);
  if (!Number.isSafeInteger(amount)) return null;
  return amount;
}

function formatTokenDraft(value: number) {
  return String(value);
}

function formatTokenHint(value: number) {
  if (!value) return "0";
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000, 2)}M`;
  if (value >= 1_000) return `${trimNumber(value / 1_000, 1)}K`;
  return String(value);
}

function trimNumber(value: number, digits: number) {
  return String(Number(value.toFixed(digits)));
}

function StatusBadge({ enabled }: { enabled: boolean }) {
  const { t } = useLocale();
  return enabled
    ? <Badge variant="outline" className="border-emerald-300 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">{t("common.active")}</Badge>
    : <Badge variant="secondary">{t("common.disabled")}</Badge>;
}

function providerProtocolLabel(protocol: string) {
  switch (protocol) {
    case "openai":
      return "OpenAI";
    case "openai-responses":
      return "OpenAI Responses";
    case "anthropic":
      return "Anthropic";
    case "gemini":
      return "Gemini";
    default:
      return protocol;
  }
}
