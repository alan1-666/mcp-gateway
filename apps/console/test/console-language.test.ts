import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import ts from "typescript";
import {
  chineseCopy,
  initialLanguage,
  languageLocale,
  persistLanguage,
  translate,
} from "../src/console-language";
import { dynamicMessageTemplates } from "../src/locale-errors";
import { languagePreferenceKey } from "../src/website-routing";

const storage = (value: string | null) => ({
  getItem: (key: string) => {
    assert.equal(key, languagePreferenceKey);
    return value;
  },
});

test("console reuses the website preference before browser language, including legacy cn", () => {
  assert.equal(initialLanguage(storage("en"), ["zh-CN"]), "en");
  assert.equal(initialLanguage(storage("zh"), ["en-US"]), "zh");
  assert.equal(initialLanguage(storage("cn")), "zh");
  assert.equal(initialLanguage(storage("invalid"), ["zh-TW"]), "zh");
  assert.equal(initialLanguage(storage(null), ["ZH-cn"]), "zh");
  assert.equal(initialLanguage(undefined, ["fr-FR", "zh-CN"]), "en");
  assert.equal(initialLanguage(), "en");
  assert.equal(
    initialLanguage(
      {
        getItem() {
          throw new Error("blocked");
        },
      },
      ["en-US"],
    ),
    "en",
  );
  assert.equal(languageLocale("zh"), "zh-CN");
  assert.equal(languageLocale("en"), "en-US");
});

test("language persistence uses only the shared preference and tolerates blocked storage", () => {
  const saved: [string, string][] = [];
  persistLanguage({ setItem: (key, value) => saved.push([key, value]) }, "zh");
  assert.deepEqual(saved, [[languagePreferenceKey, "zh"]]);
  assert.doesNotThrow(() =>
    persistLanguage(
      {
        setItem() {
          throw new Error("blocked");
        },
      },
      "en",
    ),
  );
  assert.doesNotThrow(() => persistLanguage(undefined, "zh"));
});

test("known UI copy changes language; unknown strings and protocol data remain intact", () => {
  assert.equal(translate("en", "Overview"), "Overview");
  assert.equal(translate("zh", "Overview"), "概览");
  for (const source of [
    "third_party: denied x-123",
    '{"status":"ready","cursor":"{next}"}',
    "__proto__",
    "toString",
    "constructor",
    "",
    "工具名称",
  ]) {
    assert.equal(translate("zh", source), source);
    assert.equal(translate("en", source), source);
  }
});

test("interpolation is a single pass, honors own properties, and preserves missing placeholders", () => {
  assert.equal(
    translate("zh", "Inspect {name}", { name: "<script>{id}</script>$&" }),
    "查看 <script>{id}</script>$&",
  );
  assert.equal(
    translate("en", "Last refreshed {time}", { time: "12:00" }),
    "Last refreshed 12:00",
  );
  assert.equal(translate("zh", "Inspect {name}"), "查看 {name}");
  assert.equal(translate("zh", "Inspect {name}", {}), "查看 {name}");
  assert.equal(
    translate("zh", "Inspect {name}", Object.create({ name: "inherited" })),
    "查看 {name}",
  );
  assert.equal(
    translate("zh", "{count} records loaded", { count: 0 }),
    "已加载 0 条记录",
  );
});

test("controller errors translate at render time and retain numeric identifiers", () => {
  assert.equal(
    translate("zh", "Gateway connection failed (HTTP 503)."),
    "网关连接失败（HTTP 503）",
  );
  assert.equal(translate("zh", "Request failed (502)."), "请求失败（502）");
  assert.equal(
    translate("zh", "Input schema must be valid JSON."),
    "输入结构必须是有效的 JSON",
  );
  assert.equal(
    translate("zh", "Arguments must be a JSON object."),
    "参数必须是 JSON 对象",
  );
  assert.equal(
    translate("en", "Request failed (502)."),
    "Request failed (502).",
  );
  for (const template of dynamicMessageTemplates) {
    assert.ok(
      Object.hasOwn(chineseCopy, template),
      `missing dynamic copy: ${template}`,
    );
    const text = template
      .replaceAll("{status}", "503")
      .replaceAll("{version}", "17")
      .replaceAll("{label}", "Arguments");
    assert.notEqual(translate("zh", text), text);
    assert.equal(translate("en", text), text);
  }
  const suffix =
    "The update may have completed. Reload the latest contract before saving again.";
  assert.equal(
    translate("zh", `forbidden ${suffix}`),
    `${translate("zh", "forbidden")} ${translate("zh", suffix)}`,
  );
  assert.equal(
    translate("zh", "Request failed (not a status)."),
    "Request failed (not a status).",
  );
});

test("all translations preserve named placeholders", () => {
  const parameters = (value: string) =>
    [...value.matchAll(/\{([A-Za-z][A-Za-z0-9_]*)\}/g)].map((m) => m[1]).sort();
  for (const [key, value] of Object.entries(chineseCopy)) {
    assert.ok(value.trim(), `empty translation: ${key}`);
    assert.deepEqual(
      parameters(value),
      parameters(key),
      `placeholder mismatch: ${key}`,
    );
  }
});

test("console static translation calls all have Chinese copy, excluding technical constants", () => {
  const root = new URL("../src/", import.meta.url);
  const technical = new Set([
    "JSON",
    "MCP",
    "HTTP",
    "Rillgate",
    "W",
    "null",
    "· v",
    "v",
    "UTF-8",
    "MCP · Streamable HTTP",
  ]);
  const missing: string[] = [];
  for (const name of readdirSync(root).filter((name) =>
    name.endsWith(".tsx"),
  )) {
    const source = ts.createSourceFile(
      name,
      readFileSync(new URL(name, root), "utf8"),
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TSX,
    );
    const visit = (node: ts.Node) => {
      if (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        node.expression.text === "t"
      ) {
        const key = node.arguments[0];
        if (
          key &&
          ts.isStringLiteral(key) &&
          !technical.has(key.text) &&
          !Object.hasOwn(chineseCopy, key.text)
        )
          missing.push(`${name}: ${key.text}`);
      }
      ts.forEachChild(node, visit);
    };
    visit(source);
  }
  assert.deepEqual(missing, []);
});
