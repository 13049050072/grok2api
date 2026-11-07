export const chatHistoryStoragePrefix = "grok2api:creative-console:chat-history:";

type KeyedStorage = Pick<Storage, "length" | "key" | "removeItem">;

/** 退出登录时清掉所有作用域下的创作台对话历史，避免同一浏览器的下一位管理员看到。 */
export function clearChatHistoryStorage(storage?: KeyedStorage): void {
  try {
    const target = storage ?? window.localStorage;
    // 先收集再删除：边遍历边 removeItem 会让后续索引错位而漏删。
    const keys: string[] = [];
    for (let index = 0; index < target.length; index += 1) {
      const key = target.key(index);
      if (key?.startsWith(chatHistoryStoragePrefix)) keys.push(key);
    }
    for (const key of keys) target.removeItem(key);
  } catch {
    // Storage may be unavailable, in which case no history was persisted.
  }
}
