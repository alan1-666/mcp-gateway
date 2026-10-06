/** Gateway-owned errors only; unknown upstream diagnostics retain their original wording. */
export const errorsZh: Record<string, string> = {
  "The gateway returned an unexpected response. Check the API connection.":
    "网关返回了意外响应，请检查 API 连接",
  "Workspace capacity or request rate has been reached. Wait for active work to finish, then try again.":
    "已达到工作空间容量或请求速率限制，请等待当前任务完成后重试",
  "The gateway did not respond in time. Refresh the operation record to check its current state.":
    "网关响应超时，请刷新调用记录确认当前状态",
  "Unable to reach the gateway. Check the connection and try again.":
    "无法连接网关，请检查连接后重试",
  "The request could not be completed.": "请求未能完成",
  "Request failed ({status}).": "请求失败（{status}）",
  "Gateway connection failed (HTTP {status}).": "网关连接失败（HTTP {status}）",
  "resource conflict": "记录发生冲突，请重新加载",
  "resource conflict: client was changed; reload before editing":
    "客户端已被修改，请重新加载后编辑",
  "resource conflict: client was changed; reload before rotating":
    "客户端已被修改，请重新加载后轮换密钥",
  "resource conflict: credential changed; reload before retrying":
    "凭证已被修改，请重新加载后重试",
  "resource conflict: reconciliation changed; reload before adding evidence":
    "核对记录已被修改，请重新加载后添加证据",
  "resource conflict: capacity limits changed; reload before saving":
    "容量限制已被修改，请重新加载后保存",
  "resource conflict: tool changed while this candidate was under review":
    "审核期间工具已变更，请重新加载",
  "resource conflict: upstream schema changed since discovery; discover and review again":
    "发现后上游结构已变更，请重新发现并审核",
  "authentication required": "需要验证身份，请重新登录",
  "permission denied": "没有执行此操作的权限",
  "resource not found": "未找到对应资源",
  "approval expired": "审批已过期",
  "the request could not be durably completed; query its operation before attempting another action":
    "请求未能可靠完成，请先查询调用记录，再考虑其他操作",
  unauthorized: "身份验证失败，请重新登录",
  forbidden: "没有执行此操作的权限",
  "not found": "未找到对应资源",
  "invalid request": "请求无效，请检查输入",
  "Enter a valid date and time.": "请输入有效的日期与时间",
  "Tool name must start with a letter and contain at most 64 letters, digits, dots, underscores, or hyphens.":
    "工具名称须以字母开头，最多 64 位，仅允许字母、数字、点、下划线和连字符",
  "Description must not exceed 4000 UTF-8 bytes.":
    "描述不得超过 4000 个 UTF-8 字节",
  "Read tools require the GET method.": "读取工具必须使用 GET 方法",
  "Credential references use uppercase letters, digits, and underscores, starting with a letter.":
    "凭证引用须以字母开头，仅允许大写字母、数字和下划线",
  'Input schema must declare type "object".':
    '输入结构必须声明 type 为 "object"',
  "The destination must use HTTP or HTTPS.": "目标地址必须使用 HTTP 或 HTTPS",
  "Destination URLs must not contain a fragment.": "目标 URL 不能包含片段标识",
  "Use a credential reference instead of credentials in the URL.":
    "请使用凭证引用，不要将凭证放在 URL 中",
  "Approval and execution require arguments that this console can represent exactly.":
    "审批与执行要求控制台能精确表示全部参数",
  "Search is limited to 200 UTF-8 bytes. Shorten the name or description.":
    "搜索内容最多 200 个 UTF-8 字节，请缩短名称或描述",
  "Page size must be an integer between 1 and 50.":
    "每页条数须为 1 至 50 的整数",
  "The gateway returned an invalid search cursor. Restart the search.":
    "网关返回了无效搜索游标，请重新搜索",
  "Choose a valid MCP server ID.": "请选择有效的 MCP 服务 ID",
  "The gateway returned an invalid tool page. Retry the search.":
    "网关返回的工具分页无效，请重新搜索",
  "The search cursor did not advance. Restart the search.":
    "搜索游标未推进，请重新搜索",
  "The gateway returned a different tool. Select the tool again.":
    "网关返回的工具不匹配，请重新选择",
  "This tool is no longer published and enabled. Refresh the catalog and select another tool.":
    "此工具已不再处于发布并启用状态，请刷新目录并选择其他工具",
  "The gateway returned an invalid event cursor.": "网关返回了无效事件游标",
  "Describe your task in 1–8,000 characters.": "请用 1 至 8,000 个字符描述任务",
  "{label} must be valid JSON.": "{label}必须是有效的 JSON",
  "{label} must be a JSON object.": "{label}必须是 JSON 对象",
  "{label} contains a number outside the browser's exact numeric range. Use a string for large identifiers or high-precision values, with a tool schema that accepts strings.":
    "{label}包含超出浏览器精确表示范围的数字，请使用支持字符串的工具结构，以字符串传递大数标识或高精度数值",
  "The update may have completed. Reload the latest contract before saving again.":
    "更新可能已完成，请加载最新接口定义后再保存",
};

// These messages come from locale-independent controllers. Restrict matching to
// known templates rather than rewriting arbitrary numbers or upstream content.
export const dynamicMessageTemplates: readonly string[] = [
  "Request failed ({status}).",
  "Gateway connection failed (HTTP {status}).",
  "{label} must be valid JSON.",
  "{label} must be a JSON object.",
  "{label} contains a number outside the browser's exact numeric range. Use a string for large identifiers or high-precision values, with a tool schema that accepts strings.",
  "Policy already matches version {version}; no version was added.",
  "Saved as version {version}. New operations use this policy; already prepared operations keep their existing snapshot.",
  "Loaded version {version}. Your draft and sample are retained; review them against the saved policy before saving.",
  "This tool is now at version {version}. Reload the latest contract and review your retained draft before saving.",
];
