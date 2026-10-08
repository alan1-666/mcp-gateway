import assert from "node:assert/strict";
import test from "node:test";
import { createElement as h } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { I18nProvider } from "../src/i18n";
import { OpenAPIImport } from "../src/OpenAPIImport";
import { openapiZh } from "../src/locale-openapi";

test("OpenAPI import initially offers local JSON input with no selected operation", () => {
  for (const language of ["en", "zh"] as const) {
    let called = false;
    const html = renderToStaticMarkup(
      h(I18nProvider, {
        initial: language,
        children: h(OpenAPIImport, {
          onSelect() {
            called = true;
          },
          onCancel() {},
        }),
      }),
    );
    assert.equal(called, false);
    assert.ok(
      html.includes(
        language === "zh" ? "从 OpenAPI 导入" : "Import from OpenAPI",
      ),
    );
    assert.ok(
      html.includes(
        language === "zh" ? "不会发送网络请求" : "makes no network requests",
      ),
    );
    assert.match(html, /type="file" accept="\.json,application\/json"/);
    assert.match(html, /type="submit" disabled=""/);
    assert.ok(html.includes('placeholder="https://api.example.com/v1"'));
    assert.ok(!html.includes("<pre"));
    assert.ok(!html.includes('aria-pressed="true"'));
  }
});

test("OpenAPI translation interpolation retains every placeholder", () => {
  const placeholders = (value: string) =>
    [...value.matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)]
      .map((match) => match[1])
      .sort();
  for (const [key, value] of Object.entries(openapiZh)) {
    assert.deepEqual(placeholders(key), placeholders(value), key);
    assert.ok(value.trim());
  }
});
