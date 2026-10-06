import assert from "node:assert/strict";
import test from "node:test";
import { createElement as h } from "react";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { APIClient } from "../src/api";
import { AdminAction } from "../src/admin-state";
import { I18nProvider, LanguageSwitcher } from "../src/i18n";
import {
  AdminError,
  AdminJSON,
  AdminLoading,
  SecretReveal,
  dateLabel,
} from "../src/AdminUI";
import { Account } from "../src/Account";
import { ApprovalPolicy } from "../src/ApprovalPolicy";
import { ToolConnection, ToolResponsePolicy } from "../src/ToolConnection";
import type { Tool } from "../src/types";

const tool: Tool = {
  id: "tool-fixture",
  workspace_id: "workspace-fixture",
  name: "Read",
  description: "Upstream description must stay unchanged",
  risk: "write",
  approval_policy: "required",
  input_schema: { type: "object" },
  http: {
    method: "GET",
    url: "https://example.test/v1",
    timeout_ms: 1000,
    credential_ref: "SERVICE_SECRET_REFERENCE",
  },
  response_policy: {
    include: ["/result/title"],
    max_bytes: 65536,
    artifact: { max_bytes: 524288, ttl_seconds: 3600 },
  },
  status: "published",
  enabled: true,
  version: 3,
  created_at: "2026-10-06T12:00:00Z",
};

function render(language: "en" | "zh", child: ReactNode) {
  return renderToStaticMarkup(
    h(I18nProvider, { initial: language, children: child }),
  );
}

test("language switcher exposes translated accessible names and current language", () => {
  for (const language of ["en", "zh"] as const) {
    const html = render(language, h(LanguageSwitcher));
    assert.ok(
      html.includes(`aria-label="${language === "zh" ? "语言" : "Language"}"`),
    );
    assert.match(
      html,
      new RegExp(
        `lang="${language === "zh" ? "zh-CN" : "en"}" aria-pressed="true"`,
      ),
    );
    assert.ok(html.includes(">EN</button>"));
    assert.ok(html.includes(">中文</button>"));
  }
});

test("admin feedback translates known UI errors and preserves unknown upstream messages", () => {
  const zh = render(
    "zh",
    h(AdminError, {
      error: "Enter a valid date and time.",
      onRetry() {},
    }),
  );
  assert.match(zh, /role="alert"/);
  assert.ok(zh.includes("请输入有效的日期与时间"));
  assert.ok(zh.includes("重新加载当前数据"));
  assert.ok(
    render(
      "en",
      h(AdminError, { error: "Enter a valid date and time." }),
    ).includes("Enter a valid date and time."),
  );
  const upstream = "Upstream XYZ_FAILURE_17: original supplier explanation";
  assert.ok(
    render("zh", h(AdminError, { error: upstream })).includes(upstream),
  );
  assert.ok(render("zh", h(AdminLoading)).includes("正在加载当前记录"));
});

test("secret and JSON values remain data while their surrounding labels translate", () => {
  const secret = "synthetic_only_Read_123";
  for (const language of ["en", "zh"] as const) {
    const reveal = render(language, h(SecretReveal, { secret, onClose() {} }));
    assert.ok(reveal.includes(`value="${secret}"`));
    assert.match(reveal, /type="password"/);
    assert.ok(
      reveal.includes(
        language === "zh" ? "请立即保存此访问密钥" : "Save this API key now",
      ),
    );
    const json = render(
      language,
      h(AdminJSON, {
        label: "Recorded change",
        value: {
          status: "UNKNOWN",
          name: "Read",
          note: "<script>literal data</script>",
        },
      }),
    );
    assert.ok(
      json.includes(language === "zh" ? "记录的变更" : "Recorded change"),
    );
    assert.ok(json.includes("UNKNOWN"));
    assert.ok(json.includes("Read"));
    assert.ok(!json.includes("只读"));
    assert.ok(!json.includes("<script>"));
    assert.ok(json.includes("&lt;script&gt;literal data&lt;/script&gt;"));
  }
});

test("tool connection and response policy localize labels without changing technical values", () => {
  const zh = render("zh", h(ToolConnection, { tool }));
  const en = render("en", h(ToolConnection, { tool }));
  assert.ok(zh.includes("凭证引用"));
  assert.ok(en.includes("Credential reference"));
  for (const html of [zh, en]) {
    assert.ok(html.includes(tool.http.url));
    assert.ok(html.includes("GET"));
    assert.ok(html.includes("SERVICE_SECRET_REFERENCE"));
  }
  const policy = render("zh", h(ToolResponsePolicy, { tool }));
  assert.ok(policy.includes("大结果上限为 524,288 字节，保留 3600 秒"));
  assert.ok(policy.includes("/result/title"));
  assert.ok(policy.includes("nextCursor"));
  assert.ok(policy.includes("next_cursor"));
  const timestamp = "2026-10-06T12:00:00Z";
  assert.equal(
    dateLabel(timestamp, "zh-CN"),
    new Date(timestamp).toLocaleString("zh-CN"),
  );
  assert.equal(
    dateLabel(timestamp, "en-US"),
    new Date(timestamp).toLocaleString("en-US"),
  );
  assert.notEqual(dateLabel(timestamp, "zh-CN"), dateLabel(timestamp, "en-US"));
});

test("approval policy SSR retains API enum values and uses a stable server snapshot", () => {
  const api = new APIClient();
  const controller = new AdminAction(api);
  assert.strictEqual(controller.getSnapshot(), controller.getSnapshot());
  assert.equal(controller.getSnapshot().busy, false);
  for (const language of ["en", "zh"] as const) {
    const html = render(
      language,
      h(ApprovalPolicy, { api, tool, onChanged: async () => {} }),
    );
    assert.ok(
      html.includes(
        language === "zh" ? "要求独立审批" : "Require independent approval",
      ),
    );
    assert.ok(
      html.includes(
        language === "zh"
          ? "允许已授权调用直接执行"
          : "Allow authorized calls without approval",
      ),
    );
    assert.deepEqual(
      [...html.matchAll(/<option value="([^"]+)"/g)].map((match) => match[1]),
      ["required", "none"],
    );
    assert.match(html, /<option value="required" selected=""/);
  }
});

test("team role labels translate while invitations retain canonical role values", () => {
  const html = render(
    "zh",
    h(Account, {
      api: new APIClient(),
      identity: { id: "owner-fixture", workspace_id: "team", role: "admin" },
      onSignedOut() {},
      refreshVersion: "1",
    }),
  );
  assert.match(html, /<option value="operator" selected="">操作员<\/option>/);
  assert.match(html, /<option value="admin">管理员<\/option>/);
  assert.match(html, /<option value="viewer">查看者<\/option>/);
  assert.match(html, /<option value="approver">审批人<\/option>/);
});
