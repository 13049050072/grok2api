import { ApiError, apiDownloadResponse, apiEventStream, apiRequest, type ApiDownloadResult, type PaginatedDTO } from "@/shared/api/client";
import { createObjectDecoder, createPaginatedDecoder, createValidatedDecoder, decodeBooleanResult, decodeCountResult, hasShape, isArrayOf, isBoolean, isNumber, isOneOf, isOptional, isRecordOf, isString } from "@/shared/api/decoder";
import { i18n } from "@/shared/i18n";
import type { SortOrder } from "@/shared/lib/table-sort";
import { createAccountTaskProgressController, type AccountTaskProgressDTO, type AccountTaskProgressPhase } from "@/features/accounts/account-task-progress";
import { buildExportFallbackFilename, defaultAccountExportFormat, resolveExportFilename, type AccountExportFallbackName, type AccountExportFormat } from "@/features/accounts/export-formats";

export type { AccountTaskProgressDTO } from "@/features/accounts/account-task-progress";

export type AccountProvider = "grok_build" | "grok_web" | "grok_console";
export type BuildRouteMode = "auto" | "build" | "xai";
export type AccountCleanupStatus = "cooldown" | "disabled" | "reauthRequired";

export type BillingDTO = {
  planCode?: string;
  planName?: string;
  monthlyLimit: number;
  used: number;
  remaining: number;
  onDemandCap: number;
  onDemandUsed: number;
  prepaidBalance: number;
  creditUsagePercent: number;
  isUnifiedBillingUser: boolean;
  onDemandEnabled?: boolean;
  topUpMethod?: string;
  usagePeriodType?: string;
  usagePeriodStart?: string;
  usagePeriodEnd?: string;
  billingPeriodStart?: string;
  billingPeriodEnd?: string;
  history?: BillingHistoryDTO[];
  syncedAt: string;
};

export type BillingHistoryDTO = {
  year: number;
  month: number;
  periodType?: string;
  periodStart?: string;
  periodEnd?: string;
  includedUsed: number;
  onDemandUsed: number;
  totalUsed: number;
};

export type QuotaDTO = {
  type: "free" | "paid" | "unknown";
  source: "unknown" | "upstreamBilling" | "upstreamExhaustion" | "responseModel" | "billingProfile" | "buildSuperEntitlement";
  confidence: "estimated" | "observed" | "confirmed" | "";
  status: "active" | "waitingReset" | "probing";
  unit?: "tokens" | "credits" | "percent";
  used: number;
  limit: number;
  remaining: number;
  usagePercent: number;
  limitKnown: boolean;
  windowHours?: number;
  observed: boolean;
  confirmed: boolean;
  periodStart?: string;
  periodEnd?: string;
  exhaustedAt?: string;
  nextProbeAt?: string;
  lastConfirmedAt?: string;
};

export type AccountDTO = {
  id: string;
  provider: AccountProvider;
  authType: "oauth" | "sso";
  webTier?: "auto" | "basic" | "super" | "heavy";
  webTierSyncedAt?: string;
  nsfwEnabledAt?: string;
  termsAcceptedAt?: string;
  name: string;
  email?: string;
  userId?: string;
  teamId?: string;
  enabled: boolean;
  authStatus: "active" | "reauthRequired";
  expiresAt?: string;
  refreshable: boolean;
  cloudflareCookieConfigured: boolean;
  buildSuperEntitled: boolean;
  buildRouteMode: BuildRouteMode;
  buildBotFlagged: boolean;
  /** Numeric bot_flag_source/bfs claim when risk-flagged: 1 or 2. */
  buildBotFlagSource?: number;
  egressNodeId?: string;
  egressAssignmentMode?: "manual" | "auto";
  modelSyncFailed?: boolean;
  refreshDueAt?: string;
  lastRefreshAt?: string;
  credentialUpdatedAt?: string;
  refreshFailureCount: number;
  lastRefreshErrorStatus?: number;
  lastRefreshErrorCode?: string;
  lastRefreshErrorMessage?: string;
  lastRefreshErrorResponse?: string;
  priority: number;
  maxConcurrent: number;
  minimumRemaining: number;
  failureCount: number;
  cooldownUntil?: string;
  lastError?: string;
  enabledDoesNotClearCooldown?: boolean;
  lastUsedAt?: string;
  linkedAccountId?: string;
  linkedAccountName?: string;
  linkedProvider?: "grok_build" | "grok_web";
  linkedAccounts?: LinkedAccountDTO[];
  createdAt: string;
  billing?: BillingDTO;
  quota: QuotaDTO;
  quotaWindows?: Array<{ mode: string; remaining: number; total: number; usagePercent: number; breakdown?: Array<{ productCode: number; usagePercent: number }>; windowSeconds: number; resetAt?: string; syncedAt?: string; source: "default" | "estimated" | "upstream" }>;
};

export type LinkedAccountDTO = {
  id: string;
  provider: "grok_build" | "grok_web" | "grok_console";
  name: string;
  email?: string;
  userId?: string;
};

export type AccountUpdateInput = {
  name: string;
  enabled?: boolean;
  priority: number;
  maxConcurrent: number;
  minimumRemaining: number;
  cloudflareCookies?: string;
  clearCloudflareCookies?: boolean;
  buildSuperEntitled?: boolean;
  buildRouteMode?: BuildRouteMode;
};

export type AccountSummaryDTO = {
  total: number;
  available: number;
  recovering: number;
  attention: number;
  risk: number;
  providers: Record<AccountProvider, { total: number; available: number }>;
  recovery: { cooldown: number; waitingReset: number; probing: number };
  issues: { disabled: number; reauthRequired: number };
};

export type DeviceSessionDTO = {
  sessionId: string;
  userCode: string;
  verificationUri: string;
  verificationUriComplete?: string;
  intervalSeconds: number;
  expiresAt: string;
};

export type DevicePollDTO = {
  status: "pending" | "succeeded" | "syncFailed";
  account?: AccountDTO;
  synced?: number;
  syncFailed?: number;
};

const billingHistoryValidator = hasShape({
  year: isNumber, month: isNumber, periodType: isOptional(isString), periodStart: isOptional(isString), periodEnd: isOptional(isString),
  includedUsed: isNumber, onDemandUsed: isNumber, totalUsed: isNumber,
});
const billingValidator = hasShape({
  planCode: isOptional(isString), planName: isOptional(isString), monthlyLimit: isNumber, used: isNumber, remaining: isNumber,
  onDemandCap: isNumber, onDemandUsed: isNumber, prepaidBalance: isNumber, creditUsagePercent: isNumber,
  isUnifiedBillingUser: isBoolean, onDemandEnabled: isOptional(isBoolean), topUpMethod: isOptional(isString), usagePeriodType: isOptional(isString),
  usagePeriodStart: isOptional(isString), usagePeriodEnd: isOptional(isString), billingPeriodStart: isOptional(isString),
  billingPeriodEnd: isOptional(isString), history: isOptional(isArrayOf(billingHistoryValidator)), syncedAt: isString,
});
const quotaValidator = hasShape({
  type: isOneOf("free", "paid", "unknown"), source: isOneOf("unknown", "upstreamBilling", "upstreamExhaustion", "responseModel", "billingProfile", "buildSuperEntitlement"),
  confidence: isOneOf("estimated", "observed", "confirmed", ""), status: isOneOf("active", "waitingReset", "probing"),
  unit: isOptional(isOneOf("tokens", "credits", "percent")), used: isNumber, limit: isNumber, remaining: isNumber, usagePercent: isNumber,
  limitKnown: isBoolean, windowHours: isOptional(isNumber), observed: isBoolean, confirmed: isBoolean,
  periodStart: isOptional(isString), periodEnd: isOptional(isString), exhaustedAt: isOptional(isString),
  nextProbeAt: isOptional(isString), lastConfirmedAt: isOptional(isString),
});
const quotaBreakdownValidator = hasShape({ productCode: isNumber, usagePercent: isNumber });
const quotaWindowValidator = hasShape({
  mode: isString, remaining: isNumber, total: isNumber, usagePercent: isNumber, breakdown: isOptional(isArrayOf(quotaBreakdownValidator)),
  windowSeconds: isNumber, resetAt: isOptional(isString), syncedAt: isOptional(isString), source: isOneOf("default", "estimated", "upstream"),
});
const linkedAccountValidator = hasShape({ id: isString, provider: isOneOf("grok_build", "grok_web", "grok_console"), name: isString, email: isOptional(isString), userId: isOptional(isString) });
const accountValidator = hasShape({
  id: isString, provider: isOneOf("grok_build", "grok_web", "grok_console"), authType: isOneOf("oauth", "sso"), webTier: isOptional(isOneOf("auto", "basic", "super", "heavy")),
  webTierSyncedAt: isOptional(isString), nsfwEnabledAt: isOptional(isString), termsAcceptedAt: isOptional(isString), name: isString, email: isOptional(isString), userId: isOptional(isString), teamId: isOptional(isString),
  enabled: isBoolean, authStatus: isOneOf("active", "reauthRequired"), expiresAt: isOptional(isString), refreshable: isBoolean, cloudflareCookieConfigured: isBoolean,
  buildSuperEntitled: isBoolean, buildRouteMode: isOneOf("auto", "build", "xai"), buildBotFlagged: isBoolean, buildBotFlagSource: isOptional(isNumber), modelSyncFailed: isOptional(isBoolean), refreshDueAt: isOptional(isString), lastRefreshAt: isOptional(isString), credentialUpdatedAt: isOptional(isString), refreshFailureCount: isNumber,
  egressNodeId: isOptional(isString), egressAssignmentMode: isOptional(isOneOf("manual", "auto")),
  lastRefreshErrorStatus: isOptional(isNumber), lastRefreshErrorCode: isOptional(isString), lastRefreshErrorMessage: isOptional(isString), lastRefreshErrorResponse: isOptional(isString), priority: isNumber, maxConcurrent: isNumber, minimumRemaining: isNumber,
  failureCount: isNumber, cooldownUntil: isOptional(isString), lastError: isOptional(isString), lastUsedAt: isOptional(isString),
  enabledDoesNotClearCooldown: isOptional(isBoolean),
  linkedAccountId: isOptional(isString), linkedAccountName: isOptional(isString), linkedProvider: isOptional(isOneOf("grok_build", "grok_web")), linkedAccounts: isOptional(isArrayOf(linkedAccountValidator)),
  createdAt: isString, billing: isOptional(billingValidator), quota: quotaValidator, quotaWindows: isOptional(isArrayOf(quotaWindowValidator)),
});
const decodeBilling = createValidatedDecoder<BillingDTO>("billing", billingValidator);
const decodeAccount = createValidatedDecoder<AccountDTO>("account", accountValidator);
const decodeAccountPage = createPaginatedDecoder<AccountDTO>(accountValidator);
const decodeAccountIds = createObjectDecoder<{ ids: string[] }>("account ids", { ids: isArrayOf(isString) });
const decodeAccountSummary = createObjectDecoder<AccountSummaryDTO>("account summary", {
  total: isNumber, available: isNumber, recovering: isNumber, attention: isNumber, risk: isNumber,
  providers: isRecordOf(hasShape({ total: isNumber, available: isNumber })),
  recovery: hasShape({ cooldown: isNumber, waitingReset: isNumber, probing: isNumber }),
  issues: hasShape({ disabled: isNumber, reauthRequired: isNumber }),
});
const decodeDeviceSession = createObjectDecoder<DeviceSessionDTO>("device session", {
  sessionId: isString, userCode: isString, verificationUri: isString, verificationUriComplete: isOptional(isString),
  intervalSeconds: isNumber, expiresAt: isString,
});
const decodeDevicePoll = createObjectDecoder<DevicePollDTO>("device poll", {
  status: isOneOf("pending", "succeeded", "syncFailed"), account: isOptional(accountValidator), synced: isOptional(isNumber), syncFailed: isOptional(isNumber),
});

type AccountFiltersInput = {
  search?: string;
  type?: string;
  status?: string;
  egress?: string;
  renewal?: string;
  risk?: string;
  agreement?: string;
  association?: string;
  // 为空时返回全部 provider 的账号，用于跨 provider 的通用名单（如请求审计筛选）。
  provider?: AccountProvider;
};

type ListAccountsInput = AccountFiltersInput & {
  page: number;
  pageSize: number;
  sortBy?: string;
  sortOrder?: SortOrder;
};

// 列表与导出/全选共用同一套筛选参数，避免两边口径漂移。
function appendAccountFilters(query: URLSearchParams, input: AccountFiltersInput): void {
  if (input.search) query.set("search", input.search);
  if (input.type) query.set("type", input.type);
  if (input.status) query.set("status", input.status);
  if (input.egress) query.set("egress", input.egress);
  if (input.renewal) query.set("renewal", input.renewal);
  if (input.risk) query.set("risk", input.risk);
  if (input.agreement) query.set("agreement", input.agreement);
  if (input.association) query.set("association", input.association);
  if (input.provider) query.set("provider", input.provider);
}

export function listAccounts(input: ListAccountsInput): Promise<PaginatedDTO<AccountDTO>> {
  const query = new URLSearchParams({ page: String(input.page), pageSize: String(input.pageSize) });
  appendAccountFilters(query, input);
  if (input.sortBy && input.sortOrder) {
    query.set("sortBy", input.sortBy);
    query.set("sortOrder", input.sortOrder);
  }
  return apiRequest(`/api/admin/v1/accounts?${query}`, {}, decodeAccountPage);
}

/** 全选用：按当前筛选取全部账号 id，不接受分页（后端超过 10000 个会直接 400）。 */
export function listAccountIds(input: AccountFiltersInput): Promise<{ ids: string[] }> {
  const query = new URLSearchParams();
  appendAccountFilters(query, input);
  const search = query.toString();
  return apiRequest(`/api/admin/v1/accounts/ids${search ? `?${search}` : ""}`, {}, decodeAccountIds);
}

export function getAccountSummary(): Promise<AccountSummaryDTO> {
  return apiRequest("/api/admin/v1/accounts/summary", {}, decodeAccountSummary);
}

export function updateAccount(id: string, input: AccountUpdateInput): Promise<AccountDTO> {
  return apiRequest(`/api/admin/v1/accounts/${id}`, { method: "PATCH", body: input }, decodeAccount);
}

export type LinkedDeleteTarget = AccountProvider;

export type AccountDeletionPreviewDTO = {
  rootCount: number;
  linkedByProvider: Partial<Record<AccountProvider, number>>;
  total: number;
};

export type AccountDeleteResultDTO = {
  deleted: number;
  rootsDeleted?: number;
  linkedDeleted?: number;
  // Batch paths skip whole groups that still have active media jobs.
  skipped?: number;
  deletedByProvider?: Partial<Record<AccountProvider, number>>;
};

export function deleteAccount(id: string, input?: { provider?: AccountProvider; linkedDeleteTargets?: LinkedDeleteTarget[] }): Promise<AccountDeleteResultDTO | { deleted: boolean }> {
  if (input?.linkedDeleteTargets?.length) {
    return apiRequest(
      `/api/admin/v1/accounts/${id}`,
      { method: "DELETE", body: { provider: input.provider, linkedDeleteTargets: input.linkedDeleteTargets } },
      createObjectDecoder("account delete", {
        deleted: isNumber,
        rootsDeleted: isOptional(isNumber),
        linkedDeleted: isOptional(isNumber),
        deletedByProvider: isOptional(isRecordOf(isNumber)),
      }),
    );
  }
  return apiRequest(`/api/admin/v1/accounts/${id}`, { method: "DELETE" }, decodeBooleanResult<{ deleted: boolean }>("deleted"));
}

export function previewAccountDeletion(ids: string[], provider: AccountProvider, linkedDeleteTargets: LinkedDeleteTarget[] = []): Promise<AccountDeletionPreviewDTO> {
  return apiRequest(
    "/api/admin/v1/accounts/deletion-preview",
    { method: "POST", body: { ids, provider, linkedDeleteTargets } },
    createObjectDecoder("account deletion preview", {
      rootCount: isNumber,
      linkedByProvider: isRecordOf(isNumber),
      total: isNumber,
    }),
  );
}

export function refreshAccountBilling(id: string): Promise<BillingDTO> {
  return apiRequest(`/api/admin/v1/accounts/${id}/refresh-billing`, { method: "POST" }, decodeBilling);
}

export function refreshAccountToken(id: string): Promise<AccountDTO> {
  return apiRequest(`/api/admin/v1/accounts/${id}/refresh-token`, { method: "POST" }, decodeAccount);
}

export function clearAccountCooldown(id: string): Promise<AccountDTO> {
  return apiRequest(`/api/admin/v1/accounts/${id}/clear-cooldown`, { method: "POST" }, decodeAccount);
}

export function acceptWebAccountTerms(id: string): Promise<{ completed: boolean }> {
  return apiRequest(`/api/admin/v1/accounts/web/${id}/accept-terms`, { method: "POST" }, decodeBooleanResult<{ completed: boolean }>("completed"));
}

export function setWebAccountBirthDate(id: string): Promise<{ completed: boolean }> {
  return apiRequest(`/api/admin/v1/accounts/web/${id}/birth-date`, { method: "POST" }, decodeBooleanResult<{ completed: boolean }>("completed"));
}

export function enableWebAccountNSFW(id: string): Promise<{ completed: boolean }> {
  return apiRequest(`/api/admin/v1/accounts/web/${id}/nsfw`, { method: "POST" }, decodeBooleanResult<{ completed: boolean }>("completed"));
}

export type AccountBatchResultDTO = { succeeded: number; failed: number };
export type AccountTokenRefreshResultDTO = AccountBatchResultDTO & { skipped: number };

/** 管理端 Grok Build 检测的单账号增量结果（SSE event: item）。 */
export type BuildDetectItemDTO = {
  id: string;
  name: string;
  email?: string;
  outcome: "ok" | "invalid" | "failed";
  reason?: string;
  httpStatus?: number;
};

export type BuildDetectHandlers = {
  onProgress?: (value: AccountTaskProgressDTO) => void;
  onItem?: (item: BuildDetectItemDTO) => void;
};

export type BuildConversionResultDTO = {
  created: number;
  linked: number;
  skipped: number;
  failed: number;
  synced: number;
  syncFailed: number;
};

export type AccountSyncStrategy = "missing" | "all";
export type BuildConversionStrategy = AccountSyncStrategy;
export type WebConsoleSyncStrategy = AccountSyncStrategy;

export type BuildConversionInput =
  | { all: true; ids?: never; strategy?: BuildConversionStrategy }
  | { all?: false; ids: string[]; strategy?: BuildConversionStrategy };

export type WebConsoleSyncInput =
  | { all: true; ids?: never; strategy: WebConsoleSyncStrategy }
  | { all?: false; ids: string[]; strategy: WebConsoleSyncStrategy };

export type WebAccountScriptActions = {
  acceptTerms: boolean;
  setBirthDate: boolean;
  enableNSFW: boolean;
};

export type WebAccountScriptsInput =
  | { all: true; ids?: never; actions: WebAccountScriptActions }
  | { all?: false; ids: string[]; actions: WebAccountScriptActions };

export type AccountImportResultDTO = {
  created: number;
  updated: number;
  skipped: number;
  failed: number;
  synced: number;
  syncFailed: number;
};

export type WebConsoleSyncResultDTO = AccountImportResultDTO & { skipped: number };

type AccountTaskStreamPayload = Partial<BuildConversionResultDTO & AccountTaskProgressDTO & AccountTokenRefreshResultDTO & AccountImportResultDTO & BuildDetectItemDTO> & {
  code?: string;
  message?: string;
  outcome?: string;
  reason?: string;
  httpStatus?: number;
  id?: string;
  name?: string;
  email?: string;
};

const decodeAccountTaskStreamPayload = createObjectDecoder<AccountTaskStreamPayload>("account task event", {
  created: isOptional(isNumber), linked: isOptional(isNumber), skipped: isOptional(isNumber), failed: isOptional(isNumber),
  synced: isOptional(isNumber), syncFailed: isOptional(isNumber), completed: isOptional(isNumber), total: isOptional(isNumber),
  phase: isOptional(isOneOf("importing", "converting", "syncing")), updated: isOptional(isNumber), succeeded: isOptional(isNumber),
  code: isOptional(isString), message: isOptional(isString),
  id: isOptional(isString), name: isOptional(isString), email: isOptional(isString),
  outcome: isOptional(isOneOf("ok", "invalid", "failed")), reason: isOptional(isString), httpStatus: isOptional(isNumber),
});

function hasNumericResult(value: AccountTaskStreamPayload, fields: string[]): boolean {
  return fields.every((field) => {
    const item = value[field as keyof AccountTaskStreamPayload];
    return typeof item === "number" && Number.isInteger(item) && item >= 0;
  });
}

type AccountTaskOptions = {
  onProgress?: (value: AccountTaskProgressDTO) => void;
  signal?: AbortSignal;
  phases?: readonly AccountTaskProgressPhase[];
};

const importSyncPhases = ["importing", "syncing"] as const;
const conversionSyncPhases = ["converting", "syncing"] as const;

async function runAccountTask<T>(path: string, body: BodyInit | object | undefined, resultFields: string[], options: AccountTaskOptions = {}): Promise<T> {
  let result: T | undefined;
  const progress = createAccountTaskProgressController(options);
  try {
    await apiEventStream(path, {
      method: "POST",
      headers: { Accept: "text/event-stream" },
      body,
      signal: options.signal,
    }, decodeAccountTaskStreamPayload, ({ event, data }) => {
      if (event === "progress" && typeof data.completed === "number" && typeof data.total === "number") {
        const phase = data.phase === "importing" || data.phase === "converting" || data.phase === "syncing" ? data.phase : undefined;
        progress.report({ completed: data.completed, total: data.total, phase });
        return;
      }
      if (event === "complete") {
        progress.flush();
        if (hasNumericResult(data, resultFields)) result = data as T;
        return;
      }
      if (event === "error") {
        const code = data.code ?? "accountConversionFailed";
        throw new ApiError(502, code, i18n.exists(`apiErrors.${code}`) ? i18n.t(`apiErrors.${code}`) : (data.message ?? i18n.t("apiErrors.requestFailed")));
      }
    });
  } finally {
    progress.dispose();
  }
  if (!result) {
    throw new ApiError(502, "invalidResponse", i18n.t("apiErrors.invalidResponse"));
  }
  return result;
}

export function refreshAllAccountBilling(onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/refresh-billing", undefined, ["succeeded", "failed"], { onProgress, signal });
}

export type DetectBuildAccountsInput =
  | { all: true; ids?: never }
  | { all?: false; ids: string[] };

export function detectBuildAccounts(input: DetectBuildAccountsInput, handlers?: BuildDetectHandlers | ((value: AccountTaskProgressDTO) => void), signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  const body = input.all ? { provider: "grok_build" as const, all: true } : { provider: "grok_build" as const, ids: input.ids };
  const resolved: BuildDetectHandlers = typeof handlers === "function" ? { onProgress: handlers } : (handlers ?? {});
  return runDetectBuildAccountsTask(body, resolved, signal);
}

async function runDetectBuildAccountsTask(body: object, handlers: BuildDetectHandlers, signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  let result: AccountBatchResultDTO | undefined;
  const progress = createAccountTaskProgressController({ onProgress: handlers.onProgress });
  try {
    await apiEventStream("/api/admin/v1/accounts/detect", {
      method: "POST",
      headers: { Accept: "text/event-stream" },
      body,
      signal,
    }, decodeAccountTaskStreamPayload, ({ event, data }) => {
      if (event === "progress" && typeof data.completed === "number" && typeof data.total === "number") {
        progress.report({ completed: data.completed, total: data.total });
        return;
      }
      if (event === "item" && typeof data.id === "string" && typeof data.name === "string" && (data.outcome === "ok" || data.outcome === "invalid" || data.outcome === "failed")) {
        handlers.onItem?.({
          id: data.id,
          name: data.name,
          email: data.email,
          outcome: data.outcome,
          reason: data.reason,
          httpStatus: data.httpStatus,
        });
        return;
      }
      if (event === "complete") {
        progress.flush();
        if (hasNumericResult(data, ["succeeded", "failed"])) result = data as AccountBatchResultDTO;
        return;
      }
      if (event === "error") {
        const code = data.code ?? "accountDetectFailed";
        throw new ApiError(502, code, i18n.exists(`apiErrors.${code}`) ? i18n.t(`apiErrors.${code}`) : (data.message ?? i18n.t("apiErrors.requestFailed")));
      }
    });
  } finally {
    progress.dispose();
  }
  if (!result) {
    throw new ApiError(502, "invalidResponse", i18n.t("apiErrors.invalidResponse"));
  }
  return result;
}

export function refreshAllAccountTokens(onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountTokenRefreshResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/refresh-tokens", undefined, ["succeeded", "failed", "skipped"], { onProgress, signal });
}

export function refreshAllWebAccountQuotas(onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/web/refresh-quotas", undefined, ["succeeded", "failed"], { onProgress, signal });
}

export function refreshAllConsoleAccountQuotas(onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/console/refresh-quotas", undefined, ["succeeded", "failed"], { onProgress, signal });
}

export function convertWebAccountsToBuild(input: BuildConversionInput, onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<BuildConversionResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/web/convert-to-build", input, ["created", "linked", "skipped", "failed", "synced", "syncFailed"], { onProgress, signal, phases: conversionSyncPhases });
}

export function syncWebAccountsToConsole(input: WebConsoleSyncInput, onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<WebConsoleSyncResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/web/sync-to-console", input, ["created", "updated", "skipped", "failed", "synced", "syncFailed"], { onProgress, signal, phases: importSyncPhases });
}

export function runWebAccountScripts(input: WebAccountScriptsInput, onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountBatchResultDTO> {
  return runAccountTask("/api/admin/v1/accounts/web/run-scripts", input, ["succeeded", "failed"], { onProgress, signal });
}

export function importAccounts(files: readonly File[], onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountImportResultDTO> {
  const body = new FormData();
  files.forEach((file) => body.append("files", file, file.name));
  return runAccountTask("/api/admin/v1/accounts/import", body, ["created", "updated", "skipped", "failed", "synced", "syncFailed"], { onProgress, signal, phases: importSyncPhases });
}

export function importWebAccounts(files: readonly File[], onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountImportResultDTO> {
  const body = new FormData();
  files.forEach((file) => body.append("files", file, file.name));
  return runAccountTask("/api/admin/v1/accounts/web/import", body, ["created", "updated", "skipped", "failed", "synced", "syncFailed"], { onProgress, signal, phases: importSyncPhases });
}

export function importConsoleAccounts(files: readonly File[], onProgress?: (value: AccountTaskProgressDTO) => void, signal?: AbortSignal): Promise<AccountImportResultDTO> {
  const body = new FormData();
  files.forEach((file) => body.append("files", file, file.name));
  return runAccountTask("/api/admin/v1/accounts/console/import", body, ["created", "updated", "skipped", "failed", "synced", "syncFailed"], { onProgress, signal, phases: importSyncPhases });
}

export function refreshAccountQuota(id: string): Promise<AccountDTO> {
  return apiRequest(`/api/admin/v1/accounts/${id}/refresh-quota`, { method: "POST" }, decodeAccount);
}

export type AccountExportFile = {
  blob: Blob;
  filename: string;
  /** 响应头 X-Exported-Accounts；缺失或不合法时为 undefined。 */
  count?: number;
};

function optionalExportedCount(headers: Headers): number | undefined {
  const value = headers.get("X-Exported-Accounts");
  if (value === null) return undefined;
  const count = Number(value);
  return Number.isSafeInteger(count) && count >= 0 ? count : undefined;
}

async function downloadAccountExport(path: string, body: object, fallback: AccountExportFallbackName): Promise<AccountExportFile> {
  const result: ApiDownloadResult = await apiDownloadResponse(path, { method: "POST", body });
  return {
    blob: result.blob,
    // 文件名以服务端 Content-Disposition 为准；缺失或不可解析时回退本地拼接。
    filename: resolveExportFilename(result.headers.get("Content-Disposition"), buildExportFallbackFilename(fallback)),
    count: optionalExportedCount(result.headers),
  };
}

/** POST /accounts/export：导出选中账号，单文件（json / txt / csv）。 */
export function exportSelectedAccounts(provider: AccountProvider, ids: string[], format: AccountExportFormat = defaultAccountExportFormat): Promise<AccountExportFile> {
  return downloadAccountExport("/api/admin/v1/accounts/export", { provider, ids, format }, { kind: "selected", provider, format });
}

/** POST /accounts/export/archive：全部账号按来源分文件打包成一个 ZIP。 */
export function exportAllAccountsArchive(format: AccountExportFormat = defaultAccountExportFormat): Promise<AccountExportFile> {
  return downloadAccountExport("/api/admin/v1/accounts/export/archive", { format }, { kind: "archive", format });
}

export function updateAccountsEnabled(ids: string[], enabled: boolean, provider: AccountProvider): Promise<{ updated: number }> {
  return apiRequest("/api/admin/v1/accounts/batch", { method: "PATCH", body: { ids, enabled, provider } }, decodeCountResult<{ updated: number }>("updated"));
}

export function updateAccountsMaxConcurrent(ids: string[], maxConcurrent: number, provider: AccountProvider): Promise<{ updated: number }> {
  return apiRequest("/api/admin/v1/accounts/batch", { method: "PATCH", body: { ids, maxConcurrent, provider } }, decodeCountResult<{ updated: number }>("updated"));
}

export function refreshAccountsQuota(ids: string[], provider: AccountProvider): Promise<{ succeeded: number; failed: number }> {
  return apiRequest("/api/admin/v1/accounts/batch/refresh-quotas", { method: "POST", body: { ids, provider } }, createObjectDecoder("account batch", { succeeded: isNumber, failed: isNumber }));
}

export function resetAccountsQuota(ids: string[], provider: AccountProvider): Promise<{ reset: number }> {
  return apiRequest("/api/admin/v1/accounts/batch/reset-quota", { method: "POST", body: { ids, provider } }, decodeCountResult<{ reset: number }>("reset"));
}

export function resetAllAccountQuota(): Promise<{ reset: number }> {
  return apiRequest("/api/admin/v1/accounts/reset-quota", { method: "POST" }, decodeCountResult<{ reset: number }>("reset"));
}

export function refreshAccountsTokens(ids: string[], provider: AccountProvider): Promise<AccountTokenRefreshResultDTO> {
  return apiRequest("/api/admin/v1/accounts/batch/refresh-tokens", { method: "POST", body: { ids, provider } }, createObjectDecoder("account token refresh batch", { succeeded: isNumber, failed: isNumber, skipped: isNumber }));
}

export type CleanupResultDTO = {
  deleted: number;
  rootsDeleted?: number;
  linkedDeleted?: number;
  skipped?: number;
  deletedByProvider?: Partial<Record<AccountProvider, number>>;
};

export type CleanupPreviewDTO = {
  rootsByStatus: Partial<Record<AccountCleanupStatus, number>>;
  rootCount: number;
  linkedByProvider: Partial<Record<AccountProvider, number>>;
  total: number;
};

export function cleanupAccounts(provider: AccountProvider, statuses: AccountCleanupStatus[], linkedDeleteTargets: LinkedDeleteTarget[] = []): Promise<CleanupResultDTO> {
  return apiRequest(
    "/api/admin/v1/accounts/cleanup",
    {
      method: "POST",
      body: {
        provider,
        statuses,
        ...(linkedDeleteTargets.length ? { linkedDeleteTargets } : {}),
      },
    },
    createObjectDecoder("account cleanup", {
      deleted: isNumber,
      rootsDeleted: isOptional(isNumber),
      linkedDeleted: isOptional(isNumber),
      skipped: isOptional(isNumber),
      deletedByProvider: isOptional(isRecordOf(isNumber)),
    }),
  );
}

export function previewCleanup(provider: AccountProvider, statuses: AccountCleanupStatus[], linkedDeleteTargets: LinkedDeleteTarget[] = []): Promise<CleanupPreviewDTO> {
  return apiRequest(
    "/api/admin/v1/accounts/cleanup-preview",
    { method: "POST", body: { provider, statuses, ...(linkedDeleteTargets.length ? { linkedDeleteTargets } : {}) } },
    createObjectDecoder("account cleanup preview", {
      rootsByStatus: isRecordOf(isNumber),
      rootCount: isNumber,
      linkedByProvider: isRecordOf(isNumber),
      total: isNumber,
    }),
  );
}

export function deleteAccounts(ids: string[], provider: AccountProvider, linkedDeleteTargets: LinkedDeleteTarget[] = []): Promise<AccountDeleteResultDTO> {
  // Batch delete must forward linkedDeleteTargets; omitting them falls back to root-only deletion.
  return apiRequest(
    "/api/admin/v1/accounts",
    {
      method: "DELETE",
      body: {
        ids,
        provider,
        ...(linkedDeleteTargets.length ? { linkedDeleteTargets } : {}),
      },
    },
    createObjectDecoder("account batch delete", {
      deleted: isNumber,
      rootsDeleted: isOptional(isNumber),
      linkedDeleted: isOptional(isNumber),
      skipped: isOptional(isNumber),
      deletedByProvider: isOptional(isRecordOf(isNumber)),
    }),
  );
}

export function startDeviceAuthorization(): Promise<DeviceSessionDTO> {
  return apiRequest("/api/admin/v1/accounts/device/start", { method: "POST" }, decodeDeviceSession);
}

export function pollDeviceAuthorization(sessionId: string, signal: AbortSignal): Promise<DevicePollDTO> {
  return apiRequest(`/api/admin/v1/accounts/device/${sessionId}/poll`, { method: "POST", signal }, decodeDevicePoll);
}
