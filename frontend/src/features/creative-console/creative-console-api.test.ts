import assert from "node:assert/strict";
import { it, mock } from "node:test";

import { editImage } from "./creative-console-api.ts";

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
