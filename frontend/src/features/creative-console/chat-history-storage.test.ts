import assert from "node:assert/strict";
import { it } from "node:test";

import { chatHistoryStoragePrefix, clearChatHistoryStorage } from "./chat-history-storage.ts";

function memoryStorage(entries: Record<string, string>) {
  const values = new Map(Object.entries(entries));
  return {
    values,
    get length() {
      return values.size;
    },
    key: (index: number) => [...values.keys()][index] ?? null,
    removeItem: (key: string) => {
      values.delete(key);
    },
  };
}

it("removes the chat history of every scope and keeps unrelated keys", () => {
  const storage = memoryStorage({
    [`${chatHistoryStoragePrefix}key-a`]: "[]",
    [`${chatHistoryStoragePrefix}key-b`]: "[]",
    "grok2api:language": "en",
    [`${chatHistoryStoragePrefix}key%2Fc`]: "[]",
    "grok2api:creative-console:other": "1",
  });
  clearChatHistoryStorage(storage);
  assert.deepEqual([...storage.values.keys()], ["grok2api:language", "grok2api:creative-console:other"]);
});

it("does not throw when storage is unavailable", () => {
  assert.doesNotThrow(() => clearChatHistoryStorage());
  assert.doesNotThrow(() => clearChatHistoryStorage({
    get length(): number {
      throw new Error("SecurityError");
    },
    key: () => null,
    removeItem: () => undefined,
  }));
});
