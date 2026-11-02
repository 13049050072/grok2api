import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { deleteAccounts, listAccounts, previewAccountDeletion, type AccountDTO, type AccountProvider } from "@/features/accounts/accounts-api";
import { toast } from "sonner";

async function readAccounts(provider: AccountProvider): Promise<AccountDTO[]> {
  const accounts: AccountDTO[] = [];
  const seen = new Set<string>();
  let expectedTotal: number | undefined;
  for (let page = 1; page <= 1000; page++) {
    const result = await listAccounts({ provider, page, pageSize: 200 });
    if (expectedTotal === undefined) expectedTotal = result.total;
    if (result.total !== expectedTotal || result.page !== page || (result.items.length === 0 && accounts.length < expectedTotal)) throw new Error("账号列表在检查时发生变化，请重试");
    for (const item of result.items) {
      if (seen.has(item.id)) throw new Error("账号列表在检查时发生变化，请重试");
      seen.add(item.id);
      accounts.push(item);
    }
    if (accounts.length === expectedTotal) return accounts;
    if (accounts.length > expectedTotal) throw new Error("账号列表在检查时发生变化，请重试");
  }
  throw new Error("账号数量过多，无法完成检查");
}

function date(value?: string): string {
  if (!value || Number.isNaN(Date.parse(value))) return "未知";
  return new Date(value).toLocaleString();
}

function autoDeleteCandidate(items: AccountDTO[]): AccountDTO | undefined {
  const users = new Set(items.map((item) => item.userId?.trim()).filter(Boolean));
  const teams = new Set(items.map((item) => item.teamId?.trim()).filter(Boolean));
  if (items.some((item) => !item.userId?.trim()) || users.size !== 1 || teams.size > 1) return undefined;
  return [...items].sort((a, b) => {
    const links = (a.linkedAccounts?.length ?? 0) - (b.linkedAccounts?.length ?? 0);
    if (links) return links;
    const health = Number(a.enabled && a.authStatus === "active") - Number(b.enabled && b.authStatus === "active");
    if (health) return health;
    const updatedA = Date.parse(a.credentialUpdatedAt ?? "");
    const updatedB = Date.parse(b.credentialUpdatedAt ?? "");
    if (Number.isFinite(updatedA) && Number.isFinite(updatedB) && updatedA !== updatedB) return updatedA - updatedB;
    return a.id.localeCompare(b.id, undefined, { numeric: true });
  })[0];
}

export function DuplicatePreview({ provider }: { provider: AccountProvider }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [targets, setTargets] = useState<AccountProvider[]>([]);
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["duplicate-preview", provider], queryFn: () => readAccounts(provider), enabled: open, staleTime: 0, refetchOnWindowFocus: false });
  const preview = useQuery({ queryKey: ["duplicate-deletion-preview", provider, selected, targets], queryFn: () => previewAccountDeletion(selected, provider, targets), enabled: deleteOpen && selected.length > 0, staleTime: 0, refetchOnWindowFocus: false });
  const deletion = useMutation({ mutationFn: () => deleteAccounts(selected, provider, targets), onSuccess: (result) => {
    toast.success(`已删除 ${result.deleted} 条账号${result.skipped ? `，跳过 ${result.skipped} 条` : ""}`);
    setSelected([]); setTargets([]); setDeleteOpen(false);
    void queryClient.invalidateQueries({ queryKey: ["accounts"] });
    void queryClient.invalidateQueries({ queryKey: ["duplicate-preview", provider] });
  }, onError: (error) => toast.error(`删除失败：${error.message}`) });
  const options = (["grok_build", "grok_web", "grok_console"] as AccountProvider[]).filter((value) => value !== provider);
  const label = (value: AccountProvider) => ({ grok_build: "Grok Build", grok_web: "Grok Web", grok_console: "Grok Console" })[value];
  const grouped = new Map<string, AccountDTO[]>();
  for (const account of query.data ?? []) {
    const email = account.email?.trim().toLowerCase();
    if (!email) continue;
    grouped.set(email, [...(grouped.get(email) ?? []), account]);
  }
  const duplicates = [...grouped.entries()].filter(([, items]) => items.length > 1).sort(([a], [b]) => a.localeCompare(b));
  const missing = (query.data ?? []).filter((item) => !item.email?.trim()).length;

  return <>
    <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>重复账号检查</Button>
    <Dialog open={open} onOpenChange={(value) => { if (deletion.isPending) return; setOpen(value); if (!value) { setSelected([]); setTargets([]); setDeleteOpen(false); } }}>
      <DialogContent className="max-h-[85vh] max-w-[900px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>重复账号与合并预览</DialogTitle>
          <DialogDescription>仅检查当前服务类型，按邮箱分组。凭据更新时间可能包含状态或缓存更新，不保证凭据有效；关联和有效性仍需核对。此预览不会修改账号。</DialogDescription>
        </DialogHeader>
        {query.isPending ? <p>正在检查账号…</p> : null}
        {query.isError ? <p className="text-destructive">检查失败：{query.error.message} <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>重试</Button></p> : null}
        {query.isSuccess ? <>
          <p className="text-sm">发现 {duplicates.length} 组同邮箱账号；{missing} 条缺少邮箱，无法按邮箱检查。</p>
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-sm">已选 {selected.length} 条</span>
            <Button variant="secondary" size="sm" disabled={!duplicates.length || query.isFetching} onClick={() => setSelected(duplicates.flatMap(([, items]) => items.map((item) => item.id)))}>全选</Button>
            <Button variant="secondary" size="sm" disabled={!duplicates.length || query.isFetching} onClick={() => {
              const candidates = duplicates.map(([, items]) => autoDeleteCandidate(items));
              const skipped = candidates.filter((item) => !item).length;
              setSelected(candidates.filter((item): item is AccountDTO => Boolean(item)).map((item) => item.id));
              if (skipped) toast.warning(`${skipped} 组身份不完整或冲突，未自动选择，请手动核对`);
            }}>自动选择每组一条</Button>
            <Button variant="secondary" size="sm" disabled={!selected.length || query.isFetching} onClick={() => setSelected([])}>取消选择</Button>
            <Button variant="destructive" size="sm" disabled={!selected.length || query.isFetching} onClick={() => { setTargets([]); setDeleteOpen(true); }}>删除所选</Button>
          </div>
          <p className="text-xs text-muted-foreground">自动选择的是待删除记录：每组最多选一条，优先选关联较少、状态较差的记录；请确认凭据和关联后再删除。</p>
          {duplicates.length === 0 ? <p className="text-sm text-muted-foreground">未发现同邮箱重复记录。</p> : null}
          {duplicates.map(([email, items]) => {
            const userIds = new Set(items.map((item) => item.userId?.trim()).filter(Boolean));
            const teams = new Set(items.map((item) => item.teamId?.trim()).filter(Boolean));
            const conflict = userIds.size > 1 || teams.size > 1;
            const knownTimes = items.filter((item) => item.credentialUpdatedAt && !Number.isNaN(Date.parse(item.credentialUpdatedAt)));
            const latestTime = knownTimes.length ? Math.max(...knownTimes.map((item) => Date.parse(item.credentialUpdatedAt!))) : 0;
            return <section key={email} className="space-y-2 rounded-lg border p-3">
              <div className="font-medium break-all">{email} · {items.length} 条</div>
              {conflict ? <p className="text-sm text-destructive">用户 ID 或团队 ID 不一致：可能不是同一身份，请勿直接合并。</p> : <p className="text-sm text-muted-foreground">同邮箱只是候选；缺失用户 ID 时仍需核实身份。</p>}
              {!conflict ? <p className="text-sm">凭据记录最近更新：{latestTime ? knownTimes.filter((item) => Date.parse(item.credentialUpdatedAt!) === latestTime).map((item) => `#${item.id}`).join("、") : "未知"}。无法仅凭现有信息确定最完整或有效的凭据，请比较各项状态和关联。</p> : null}
              {items.map((item) => <div key={item.id} className="rounded border p-2 text-sm space-y-1">
                <label className="flex items-start gap-2"><Checkbox checked={selected.includes(item.id)} onCheckedChange={(checked) => setSelected((current) => checked === true ? [...current, item.id] : current.filter((id) => id !== item.id))} /><span className="font-medium break-all">{item.name} · ID {item.id} {latestTime && Date.parse(item.credentialUpdatedAt ?? "") === latestTime ? "· 凭据记录最近更新" : ""}</span></label>
                <div>状态：{item.authStatus === "active" ? "正常" : "需重新授权"} / {item.enabled ? "已启用" : "已停用"}；类型：{item.authType}；可刷新：{item.refreshable ? "是" : "否"}；Cloudflare Cookie：{item.cloudflareCookieConfigured ? "有" : "无"}</div>
                <div>凭据记录更新：{date(item.credentialUpdatedAt)}；上次刷新：{date(item.lastRefreshAt)}；到期：{date(item.expiresAt)}</div>
                <div className="break-all">用户 ID：{item.userId || "未知"}；团队 ID：{item.teamId || "未知"}</div>
                <div className="break-all">关联：{item.linkedAccounts?.length ? item.linkedAccounts.map((link) => `${link.provider} #${link.id}`).join("、") : "无"}</div>
              </div>)}
              <p className="text-xs text-muted-foreground">候选仅供参考，关联数与凭据完整性无法互相替代；更新时间也不等同凭据签发时间。保留和删除前请逐项核对。</p>
            </section>;
          })}
        </> : null}
      </DialogContent>
    </Dialog>
    <AlertDialog open={deleteOpen} onOpenChange={(value) => { if (!deletion.isPending) setDeleteOpen(value); }}>
      <AlertDialogContent>
        <AlertDialogHeader><AlertDialogTitle>删除所选 {selected.length} 个账号？</AlertDialogTitle><AlertDialogDescription>账号凭据和额度快照将从本服务删除，此操作无法撤销。请先确认不会删除需要保留的记录。</AlertDialogDescription></AlertDialogHeader>
        <div className="space-y-3 border-t pt-3"><p className="text-sm font-medium">同时删除关联账号？</p>
          <div className="flex flex-wrap gap-4">{options.map((target) => <label key={target} className="flex items-center gap-2 text-sm"><Checkbox checked={targets.includes(target)} onCheckedChange={(checked) => setTargets((current) => checked === true ? [...current, target] : current.filter((item) => item !== target))} />{label(target)}</label>)}</div>
          {preview.isFetching ? <p className="text-sm">正在检查影响…</p> : preview.isError ? <p className="text-sm text-destructive">无法预览删除范围，请重试。<Button size="sm" variant="ghost" onClick={() => void preview.refetch()}>重试</Button></p> : preview.data ? <p className="text-sm">预计删除 {preview.data.total} 条：所选 {preview.data.rootCount} 条{targets.map((target) => `，关联 ${label(target)} ${preview.data?.linkedByProvider[target] ?? 0} 条`).join("")}。</p> : null}
        </div>
        <AlertDialogFooter><AlertDialogCancel disabled={deletion.isPending}>取消</AlertDialogCancel><AlertDialogAction className="bg-destructive text-white" disabled={deletion.isPending || preview.isFetching || preview.isError || !preview.data || preview.data.rootCount !== selected.length} onClick={(event) => { event.preventDefault(); deletion.mutate(); }}>确认删除</AlertDialogAction></AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </>;
}
