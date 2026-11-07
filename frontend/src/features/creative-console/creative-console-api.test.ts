import assert from "node:assert/strict";
import { it, mock } from "node:test";

import { editImage, publicApiURL, publicWebSocketURL } from "./creative-console-api.ts";

it("submits a local image to the image editing endpoint and resolves the result", async () => {
  const fetchMock = mock.method(globalThis, "fetch", async (url: string | URL | Request, init?: RequestInit) => {
    assert.equal(url, "/v1/images/edits");
    assert.equal(new Headers(init?.headers).get("Authorization"), "Bearer test-key");
    assert.deepEqual(JSON.parse(String(init?.body)), {
      model: "grok-imagine-image-edit", prompt: "make it blue",
      image: { url: "data:image/png;base64,YWJj" }, n: 1,
      aspect_ratio: "1:1", resolution: "1k", response_format: "url", stream: false,
    });
    return Response.json({ data: [{ url: "/v1/media/images/result" }] });
  });
  try {
    assert.deepEqual(await editImage({ apiKey: "test-key", model: "grok-imagine-image-edit", prompt: "make it blue", imageURL: "data:image/png;base64,YWJj", aspectRatio: "1:1" }), [{ url: "/v1/media/images/result", revisedPrompt: undefined }]);
    assert.equal(fetchMock.mock.callCount(), 1);
  } finally {
    fetchMock.mock.restore();
  }
});

it("surfaces an image edit API error", async () => {
  const fetchMock = mock.method(globalThis, "fetch", async () => Response.json({ error: { message: "image too large" } }, { status: 413 }));
  try {
    await assert.rejects(editImage({ apiKey: "test-key", model: "model", prompt: "edit", imageURL: "data:image/png;base64,YWJj", aspectRatio: "1:1" }), /image too large/);
  } finally {
    fetchMock.mock.restore();
  }
});

it("references a staged input file id instead of inlining the image", async () => {
  const fetchMock = mock.method(globalThis, "fetch", async (url: string | URL | Request, init?: RequestInit) => {
    assert.equal(url, "/v1/images/edits");
    assert.deepEqual(JSON.parse(String(init?.body)), {
      model: "grok-imagine-image-edit", prompt: "make it blue",
      image: { file_id: "input_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }, n: 1,
      resolution: "1k", response_format: "url", stream: false,
    });
    return Response.json({ data: [{ url: "/v1/media/images/result" }] });
  });
  try {
    assert.deepEqual(await editImage({ apiKey: "test-key", model: "grok-imagine-image-edit", prompt: "make it blue", imageFileID: "input_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }), [{ url: "/v1/media/images/result", revisedPrompt: undefined }]);
    assert.equal(fetchMock.mock.callCount(), 1);
  } finally {
    fetchMock.mock.restore();
  }
});

it("rejects an image edit request without any input image", async () => {
  await assert.rejects(editImage({ apiKey: "test-key", model: "model", prompt: "edit" }), /did not contain an input image/);
});

it("keeps same-origin /v1 paths when apiBaseUrl is empty", () => {
  assert.equal(publicApiURL("/responses", ""), "/v1/responses");
  assert.equal(publicWebSocketURL("/realtime", "", "https://admin.example.com/creative-console").toString(), "wss://admin.example.com/v1/realtime");
  assert.equal(publicWebSocketURL("/realtime", "", "http://localhost:5173/creative-console").toString(), "ws://localhost:5173/v1/realtime");
});

it("prefixes a relative apiBaseUrl and resolves it against the page for the realtime socket", () => {
  assert.equal(publicApiURL("/tts", "/grok"), "/grok/v1/tts");
  assert.equal(publicApiURL("/tts", "/grok/"), "/grok/v1/tts");
  assert.equal(publicWebSocketURL("/realtime", "/grok", "https://admin.example.com/creative-console").toString(), "wss://admin.example.com/grok/v1/realtime");
});

it("uses an absolute apiBaseUrl as is and maps its scheme for the realtime socket", () => {
  assert.equal(publicApiURL("/stt", "https://api.example.com/grok/"), "https://api.example.com/grok/v1/stt");
  assert.equal(publicWebSocketURL("/realtime", "https://api.example.com/grok", "http://admin.local/").toString(), "wss://api.example.com/grok/v1/realtime");
  assert.equal(publicWebSocketURL("/realtime", "http://10.0.0.2:8000", "https://admin.example.com/").toString(), "ws://10.0.0.2:8000/v1/realtime");
});

it("reads apiBaseUrl from the runtime config for requests, media links and the realtime socket", async () => {
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { __GROK2API_RUNTIME_CONFIG__: { apiBaseUrl: "/grok/" }, location: { origin: "https://admin.example.com", href: "https://admin.example.com/creative-console" } },
  });
  const fetchMock = mock.method(globalThis, "fetch", async (url: string | URL | Request) => {
    assert.equal(url, "/grok/v1/images/edits");
    return Response.json({ data: [{ url: "http://127.0.0.1:8000/v1/media/images/result" }] });
  });
  try {
    assert.deepEqual(await editImage({ apiKey: "test-key", model: "model", prompt: "edit", imageURL: "data:image/png;base64,YWJj" }), [{ url: "/grok/v1/media/images/result", revisedPrompt: undefined }]);
    assert.equal(fetchMock.mock.callCount(), 1);
    assert.equal(publicWebSocketURL("/realtime").toString(), "wss://admin.example.com/grok/v1/realtime");
  } finally {
    fetchMock.mock.restore();
    Reflect.deleteProperty(globalThis, "window");
  }
});
