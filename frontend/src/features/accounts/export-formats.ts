/** 账号导出的三种文件格式，与后端 POST /accounts/export 的 format 字段一致。 */
export type AccountExportFormat = "json" | "txt" | "csv";

/** 导出范围：全部账号走 ZIP，仅选中走单文件。 */
export type AccountExportScope = "all" | "selected";

export const accountExportFormats: readonly AccountExportFormat[] = ["json", "txt", "csv"];

export const defaultAccountExportFormat: AccountExportFormat = "json";

const exportFormatExtensions: Record<AccountExportFormat, string> = { json: "json", txt: "txt", csv: "csv" };

export type AccountExportFallbackName =
  | { kind: "selected"; provider: string; format: AccountExportFormat; now?: Date }
  | { kind: "archive"; format: AccountExportFormat; now?: Date };

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

/** 后端文件名里的 UTC 时间戳：YYYYMMDDTHHmmssZ（Go 的 20060102T150405Z）。 */
export function formatExportTimestamp(date: Date): string {
  const at = Number.isNaN(date.getTime()) ? new Date(0) : date;
  return `${at.getUTCFullYear()}${pad(at.getUTCMonth() + 1)}${pad(at.getUTCDate())}T${pad(at.getUTCHours())}${pad(at.getUTCMinutes())}${pad(at.getUTCSeconds())}Z`;
}

/**
 * 本地拼接的兜底文件名，仅在响应缺少可解析的 Content-Disposition 时使用。
 * 与后端命名口径保持一致（provider 原样拼接，保留 grok_web 这类下划线）：
 *   archive  → grok2api-accounts-<UTC>.zip
 *   selected → grok2api-<provider>-accounts-selected-<UTC>.<ext>
 */
export function buildExportFallbackFilename(input: AccountExportFallbackName): string {
  const timestamp = formatExportTimestamp(input.now ?? new Date());
  if (input.kind === "archive") return `grok2api-accounts-${timestamp}.zip`;
  return `grok2api-${input.provider}-accounts-selected-${timestamp}.${exportFormatExtensions[input.format]}`;
}

function sanitizeFilename(value: string): string | undefined {
  // 只看最后一段，去掉路径穿越与控制字符；后端只给文件名，前端不得接受目录。
  const base = value.replaceAll("\\", "/").split("/").pop() ?? "";
  const cleaned = [...base].filter((char) => !isControlCharacter(char)).join("").trim();
  if (!cleaned || cleaned === "." || cleaned === "..") return undefined;
  return cleaned.slice(0, 200);
}

/** C0 控制字符（U+0000–U+001F）与 DEL（U+007F）。 */
function isControlCharacter(char: string): boolean {
  const code = char.charCodeAt(0);
  return code <= 0x1f || code === 0x7f;
}

function decodeExtendedFilename(value: string): string | undefined {
  const charsetEnd = value.indexOf("'");
  if (charsetEnd < 0) return undefined;
  if (!/^utf-8$/i.test(value.slice(0, charsetEnd).trim())) return undefined;
  const languageEnd = value.indexOf("'", charsetEnd + 1);
  const encoded = languageEnd >= 0 ? value.slice(languageEnd + 1) : value.slice(charsetEnd + 1);
  try {
    return decodeURIComponent(encoded);
  } catch {
    return undefined;
  }
}

/** 解析 Content-Disposition 参数，支持 quoted-string 与转义引号。 */
function parseHeaderParameters(value: string): Map<string, string> {
  const params = new Map<string, string>();
  let index = 0;
  while (index < value.length) {
    while (index < value.length && (value[index] === ";" || /\s/.test(value[index]))) index += 1;
    const nameStart = index;
    while (index < value.length && value[index] !== "=" && value[index] !== ";") index += 1;
    const name = value.slice(nameStart, index).trim().toLowerCase();
    if (index >= value.length || value[index] !== "=") {
      if (name && !params.has(name)) params.set(name, "");
      continue;
    }
    index += 1;
    while (index < value.length && /\s/.test(value[index])) index += 1;
    let raw = "";
    if (value[index] === '"') {
      index += 1;
      while (index < value.length) {
        const char = value[index];
        if (char === "\\" && index + 1 < value.length) {
          raw += value[index + 1];
          index += 2;
          continue;
        }
        if (char === '"') {
          index += 1;
          break;
        }
        raw += char;
        index += 1;
      }
    } else {
      const rawStart = index;
      while (index < value.length && value[index] !== ";") index += 1;
      raw = value.slice(rawStart, index).trim();
    }
    if (name && !params.has(name)) params.set(name, raw);
  }
  return params;
}

/**
 * 从 Content-Disposition 里取下载文件名，优先 RFC 6266 的 filename*，其次 filename。
 * 解析失败或文件名不安全时返回 undefined，由调用方回退到本地拼接。
 */
export function parseContentDispositionFilename(header: string | null | undefined): string | undefined {
  const trimmed = header?.trim();
  if (!trimmed) return undefined;
  const separator = trimmed.indexOf(";");
  const dispositionType = separator >= 0 ? trimmed.slice(0, separator).trim().toLowerCase() : "";
  if (dispositionType && dispositionType !== "attachment" && dispositionType !== "inline") return undefined;
  // 没有分号时整段都可能只是参数（例如只有 filename），先按参数解析。
  const params = parseHeaderParameters(separator >= 0 ? trimmed.slice(separator + 1) : trimmed);
  const extended = params.get("filename*");
  if (extended) {
    const decoded = decodeExtendedFilename(extended);
    const sanitized = decoded === undefined ? undefined : sanitizeFilename(decoded);
    if (sanitized) return sanitized;
  }
  const plain = params.get("filename");
  if (!plain) return undefined;
  return sanitizeFilename(plain);
}

/** 下载文件名统一在这里定：后端 Content-Disposition 优先，失败回退本地拼接。 */
export function resolveExportFilename(header: string | null | undefined, fallback: string): string {
  return parseContentDispositionFilename(header) ?? fallback;
}

/** 「全选」用返回的 ids 整体替换当前选择，空字符串与非字符串已经被解码器挡掉。 */
export function withSelectedIds(ids: readonly string[]): Set<string> {
  const selection = new Set<string>();
  for (const id of ids) {
    if (id) selection.add(id);
  }
  return selection;
}
