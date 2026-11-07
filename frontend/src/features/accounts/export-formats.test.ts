import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  accountExportFormats,
  buildExportFallbackFilename,
  defaultAccountExportFormat,
  formatExportTimestamp,
  parseContentDispositionFilename,
  resolveExportFilename,
  withSelectedIds,
} from "./export-formats.ts";

describe("account export formats", () => {
  it("offers json, txt and csv with json as the default", () => {
    assert.deepEqual([...accountExportFormats], ["json", "txt", "csv"]);
    assert.equal(defaultAccountExportFormat, "json");
  });
});

describe("export fallback filenames", () => {
  const now = new Date(Date.UTC(2026, 10, 4, 18, 6, 58));

  it("formats the timestamp in UTC like the backend", () => {
    assert.equal(formatExportTimestamp(now), "20261104T180658Z");
  });

  it("names the archive with the zip extension regardless of format", () => {
    assert.equal(
      buildExportFallbackFilename({ kind: "archive", format: "csv", now }),
      "grok2api-accounts-20261104T180658Z.zip",
    );
  });

  it("names a selected export after the provider and format", () => {
    assert.equal(
      buildExportFallbackFilename({ kind: "selected", provider: "grok_web", format: "txt", now }),
      "grok2api-grok_web-accounts-selected-20261104T180658Z.txt",
    );
  });

  it("keeps the provider slug verbatim and marks the csv extension", () => {
    assert.equal(
      buildExportFallbackFilename({ kind: "selected", provider: "grok_console", format: "csv", now }),
      "grok2api-grok_console-accounts-selected-20261104T180658Z.csv",
    );
  });

  it("falls back to the epoch stamp for an invalid date", () => {
    assert.equal(formatExportTimestamp(new Date(Number.NaN)), "19700101T000000Z");
  });
});

describe("content-disposition filenames", () => {
  it("reads a quoted filename", () => {
    assert.equal(
      parseContentDispositionFilename('attachment; filename="grok2api-accounts-20261104-180658.zip"'),
      "grok2api-accounts-20261104-180658.zip",
    );
  });

  it("reads an unquoted filename", () => {
    assert.equal(
      parseContentDispositionFilename("attachment;filename=grok2api-grok-build-accounts-selected-20261104-180658.json"),
      "grok2api-grok-build-accounts-selected-20261104-180658.json",
    );
  });

  it("prefers the RFC 6266 filename* parameter and decodes UTF-8", () => {
    assert.equal(
      parseContentDispositionFilename("attachment; filename=\"fallback.zip\"; filename*=UTF-8''grok2api-%E8%B4%A6%E5%8F%B7.zip"),
      "grok2api-账号.zip",
    );
  });

  it("accepts lowercase filename* with a language tag", () => {
    assert.equal(
      parseContentDispositionFilename("attachment;filename*=utf-8'en'grok2api-accounts-20261104-180658.csv"),
      "grok2api-accounts-20261104-180658.csv",
    );
  });

  it("keeps escaped quotes inside the quoted filename", () => {
    assert.equal(
      parseContentDispositionFilename('attachment; filename="grok\\"quoted\\".txt"'),
      'grok"quoted".txt',
    );
  });

  it("strips directories from a hostile filename", () => {
    assert.equal(parseContentDispositionFilename('attachment; filename="../../etc/passwd"'), "passwd");
  });

  it("strips control characters", () => {
    assert.equal(parseContentDispositionFilename('attachment; filename="gro\u0000k-accounts.csv"'), "grok-accounts.csv");
  });

  it("strips the whole C0 range and DEL but keeps printable unicode", () => {
    assert.equal(parseContentDispositionFilename('attachment; filename="\u0001账号\u001f-\u007fexport\t\n.csv"'), "账号-export.csv");
    assert.equal(parseContentDispositionFilename("attachment; filename*=UTF-8''%E8%B4%A6%E5%8F%B7%7F%1F-%F0%9F%93%A6.zip"), "账号-📦.zip");
  });

  it("rejects a filename made only of control characters", () => {
    assert.equal(parseContentDispositionFilename('attachment; filename="\u0001\u007f"'), undefined);
  });

  it("rejects dot-only, empty and missing filenames", () => {
    assert.equal(parseContentDispositionFilename('attachment; filename=".."'), undefined);
    assert.equal(parseContentDispositionFilename('attachment; filename=""'), undefined);
    assert.equal(parseContentDispositionFilename("attachment"), undefined);
    assert.equal(parseContentDispositionFilename(null), undefined);
    assert.equal(parseContentDispositionFilename(undefined), undefined);
  });

  it("ignores extended values that are not valid UTF-8 percent encoding", () => {
    assert.equal(parseContentDispositionFilename("attachment; filename*=UTF-8''broken%ZZ.zip"), undefined);
  });

  it("resolves the header filename first and the fallback second", () => {
    assert.equal(resolveExportFilename('attachment; filename="server.zip"', "local.zip"), "server.zip");
    assert.equal(resolveExportFilename(null, "local.zip"), "local.zip");
    assert.equal(resolveExportFilename("attachment", "local.zip"), "local.zip");
  });
});

describe("select-all id merge", () => {
  it("replaces the selection with the returned ids", () => {
    const selection = withSelectedIds(["1", "2", "2", ""]);
    assert.deepEqual([...selection], ["1", "2"]);
  });

  it("returns an empty selection for no ids", () => {
    assert.equal(withSelectedIds([]).size, 0);
  });
});
