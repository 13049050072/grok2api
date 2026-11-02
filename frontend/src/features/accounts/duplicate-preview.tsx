import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { listAccounts, type AccountDTO, type AccountProvider } from "@/features/accounts/accounts-api";

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

export function DuplicatePreview({ provider }: { provider: AccountProvider }) {
  const [open, setOpen] = useState(false);
  const query = useQuery({ queryKey: ["duplicate-preview", provider], queryFn: () => readAccounts(provider), enabled: open, staleTime: 0, refetchOnWindowFocus: false });
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
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[85vh] max-w-[900px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>重复账号与合并预览</DialogTitle>
          <DialogDescription>仅检查当前服务类型，按邮箱分组。凭据更新时间可能包含状态或缓存更新，不保证凭据有效；关联和有效性仍需核对。此预览不会修改账号。</DialogDescription>
        </DialogHeader>
        {query.isPending ? <p>正在检查账号…</p> : null}
        {query.isError ? <p className="text-destructive">检查失败：{query.error.message} <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>重试</Button></p> : null}
        {query.isSuccess ? <>
          <p className="text-sm">发现 {duplicates.length} 组同邮箱账号；{missing} 条缺少邮箱，无法按邮箱检查。</p>
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
                <div className="font-medium break-all">{item.name} · ID {item.id} {latestTime && Date.parse(item.credentialUpdatedAt ?? "") === latestTime ? "· 凭据记录最近更新" : ""}</div>
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
  </>;
}
