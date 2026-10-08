/** MCP administration copy; upstream names, schemas, and values stay unchanged. */
export const mcpZh: Record<string, string> = {
  "Add MCP server": "添加 MCP 服务",
  "Add an MCP server": "添加 MCP 服务",
  "Add server": "添加服务",
  "Adding server…": "正在添加服务…",
  "Advertised scopes:": "服务声明的权限范围：",
  "Already imported into the tool registry. Review its draft or publication status there.":
    "已导入工具目录，可在目录中查看草稿或发布状态",
  "An authorization attempt is pending. Connecting again replaces that attempt.":
    "已有待完成的授权，重新连接会替换本次授权",
  "An exchange is in progress. Refresh status to check its result before connecting again.":
    "正在交换授权信息，请刷新状态确认结果后再连接",
  "Array selection preserves order and element count. If any element lacks a selected field, the result is rejected. Top-level nextCursor and next_cursor are preserved when present; include other pagination fields explicitly. Text is rebuilt from the retained result.":
    "数组裁剪保留原有顺序和元素数量，任一元素缺少所选字段时会拒绝结果；顶层 nextCursor 和 next_cursor 会自动保留，其他分页字段需显式添加；文本根据保留的结果重新生成",
  Authorization: "授权",
  "Authorization provider": "授权服务商",
  "Authorize the provider again to resume tool calls. Failed calls are not replayed automatically.":
    "请重新授权服务商以恢复工具调用，失败的调用不会自动重放",
  "Authorize this MCP server with its provider. Imported tools share this connection within the workspace.":
    "为此 MCP 服务完成服务商授权，工作区内导入的工具共享此连接",
  Cancel: "取消",
  "Candidate saved. Open the registry and select it under Versions & candidates to review the field changes.":
    "候选版本已保存，请在工具目录的“版本与候选”中选择它并审查字段变更",
  "Catalog checks": "目录检查",
  "Catalog history": "目录历史",
  "Change reason": "变更原因",
  "Check every (minutes)": "检查间隔（分钟）",
  "Check for new, changed, or removed tools automatically. Review changes in Activity before publishing a tool version.":
    "自动检查新增、变更或移除的工具，发布工具版本前请在“活动”中审查变更",
  "Checking connection…": "正在检查连接…",
  "Checks inspect connectivity and tool compatibility without invoking a business tool.":
    "检查连接状态和工具兼容性，不执行实际业务工具",
  "Choose a connector": "选择连接器",
  "Choose an advertised target": "选择连接器声明的目标",
  "Clear the stored tokens and pending authorization for this server? Tool calls will require a new connection. Consent at the provider is unchanged.":
    "清除此服务保存的令牌和待完成授权？工具调用将需要重新连接，服务商侧的授权同意保持不变",
  "Client ID": "客户端 ID",
  "Client authentication": "客户端认证",
  "Client secret": "客户端密钥",
  "Client secret · HTTP Basic + PKCE": "客户端密钥 · HTTP Basic + PKCE",
  "Close and discard": "关闭并清除",
  "Close form": "关闭表单",
  "Compared with registered tools": "与已注册工具比较",
  Comparison: "比较结果",
  Compatibility: "兼容性",
  "Configure OAuth": "配置 OAuth",
  "Confirm disconnect": "确认断开",
  "Confirm revocation": "确认撤销",
  "Connect a Streamable HTTP endpoint or a private connector target to discover its tools.":
    "连接 Streamable HTTP 服务或内网连接器目标，发现可用工具",
  "Connect provider": "连接服务商",
  "Connection diagnostics": "连接诊断",
  "Fresh-session contracts": "跨会话工具契约",
  "Stable in two observed sessions": "两次会话中保持一致",
  "Contract changed between sessions": "会话之间工具契约发生变化",
  "Fresh session could not be verified": "无法验证新会话",
  "Not checked": "未检查",
  "A fresh-session catalog could not be verified; reconnect compatibility is unknown.":
    "无法验证新会话的工具目录，重新连接后的兼容性尚不确定",
  "This tool changed or disappeared in a fresh session; its reviewed contract cannot be reused safely.":
    "此工具在新会话中发生变化或消失，无法安全复用已审核的契约",
  "The tool catalog changed between independent sessions; review upstream contract stability before publishing.":
    "工具目录在独立会话之间发生变化，发布前请检查上游契约是否稳定",
  "Connection and two independent catalogs passed; no business tool was executed.":
    "连接及两个独立会话的目录检查通过，未执行业务工具",
  "The connection check exceeded its deadline; contract compatibility remains unverified.":
    "连接检查超时，工具契约兼容性尚未验证",
  "The connection check was cancelled; contract compatibility remains unverified.":
    "连接检查已取消，工具契约兼容性尚未验证",
  "The upstream rate-limited the connection check; try again later.":
    "上游对连接检查进行了限流，请稍后重试",
  "The upstream was unavailable during the connection check; try again later.":
    "连接检查期间上游不可用，请稍后重试",
  "The endpoint did not accept the required MCP request; check its URL and transport.":
    "端点不接受所需的 MCP 请求，请检查地址及传输方式",
  "Connection type": "连接类型",
  Connections: "服务连接",
  Connector: "连接器",
  "Connector ID:": "连接器 ID：",
  "Connector ID": "连接器 ID",
  "Connector name": "连接器名称",
  "Connector registration token": "连接器注册令牌",
  Connectors: "连接器",
  "Copy token": "复制令牌",
  "Create registration": "创建注册",
  "Create review candidate": "创建待审查版本",
  "Creating candidate…": "正在创建候选版本…",
  "Credential reference": "凭证引用",
  "Default 65,536 bytes. Responses beyond this limit are rejected.":
    "默认 65,536 字节，超过此限制的响应会被拒绝",
  Detail: "详情",
  "Direct HTTP": "直连 HTTP",
  Disconnect: "断开连接",
  "Disconnecting…": "正在断开…",
  "Discover tools": "发现工具",
  "Discover tools again to check the previous import.":
    "请重新发现工具，确认上一次导入结果",
  "Discovered schema hash": "发现的接口定义哈希",
  "Discovering provider…": "正在发现服务商…",
  "Discovering…": "正在发现…",
  "Edit configuration": "编辑配置",
  "Enable scheduled checks": "启用定时检查",
  "Enter it again when saving configuration. Stored secrets are never returned.":
    "保存配置时请重新输入，已保存的密钥不会返回",
  "Gateway name": "网关工具名称",
  "Hide catalog history": "收起目录历史",
  "Hide checks": "收起检查",
  "Hide token": "隐藏令牌",
  "Import draft tool": "导入工具草稿",
  "Importing…": "正在导入…",
  "In progress": "进行中",
  "Input schema": "输入结构",
  "Last 50 successful discoveries. These are historical observations, not live health. Failed or incomplete discovery creates no comparison. Reload history for new scheduled results, or discover tools to check now.":
    "展示最近 50 次成功发现的历史记录，不代表实时健康状态；失败或未完成的发现不会生成比较记录；刷新历史可查看新的定时结果，也可立即发现工具",
  "Last contact:": "最近联系：",
  "Last successful check": "最近成功检查",
  "Loading MCP servers…": "正在加载 MCP 服务…",
  "Loading OAuth status…": "正在加载 OAuth 状态…",
  "Loading connectors…": "正在加载连接器…",
  "Loading schedule…": "正在加载计划…",
  "MCP connections": "MCP 服务连接",
  "MCP servers": "MCP 服务",
  "MCP tool discovery": "MCP 工具发现",
  Manual: "手动",
  "Maximum response size (bytes)": "最大响应大小（字节）",
  "NEW CONNECTION": "新建连接",
  Namespace: "命名空间",
  "Next check": "下次检查",
  "No catalog comparisons recorded.": "暂无目录比较记录",
  "No checks recorded.": "暂无检查记录",
  "No description provided by this server.": "此服务未提供说明",
  "No private connectors": "暂无内网连接器",
  "No servers connected": "尚未连接服务",
  "Not checked yet": "尚未检查",
  "Not connected yet": "尚未连接",
  "OAuth connection": "OAuth 连接",
  "OAuth connections require a cloud browser session. Sign in to the cloud workspace to authorize a provider.":
    "OAuth 连接需要云端浏览器会话，请登录云端工作区后授权服务商",
  "One field path per line, up to 32. Leave blank to keep all fields. Use /results/*/title to retain title from every array element, or /results to keep the whole array. Numeric segments refer to object keys, never array indices.":
    "每行一个字段路径，最多 32 个，留空保留全部字段；使用 /results/*/title 保留各数组元素的 title，或 /results 保留完整数组；数字路径段表示对象键，不是数组索引",
  "Open catalog history": "查看目录历史",
  "Open connection checks": "查看连接检查",
  "Open the registry to check whether the previous request saved a candidate. Discover tools again before retrying.":
    "请打开工具目录确认上一次请求是否已保存候选版本，重试前请重新发现工具",
  "Open tool registry": "打开工具库",
  "Opening provider…": "正在打开服务商…",
  "Order operations": "订单操作",
  "Output schema": "输出结构",
  "Permanently revoke this connector and block its targets?":
    "永久撤销此连接器并阻止访问其目标？",
  "Private connections · Outbound only": "内网连接 · 仅出站连接",
  "Private connector": "内网连接器",
  "Private connectors": "内网连接器",
  Provider: "服务商",
  "Provider configuration": "服务商配置",
  "Provider default": "服务商默认值",
  "Provider endpoints": "服务商接口地址",
  "Public client · PKCE": "公共客户端 · PKCE",
  "REVIEW TOOL CONTRACT": "审查工具契约",
  "Read — I confirm no external state changes": "只读 — 确认不会改变外部状态",
  "Reconnect provider": "重新连接服务商",
  "Reconnecting replaces the current grant. Tool calls pause until authorization is complete.":
    "重新连接会替换当前授权，授权完成前工具调用将暂停",
  "Reference an operator-configured credential. Never paste a secret. Leave empty to configure OAuth in server Settings.":
    "引用运维人员配置的凭据，请勿粘贴密钥；留空后可在服务设置中配置 OAuth",
  "Refresh connectors": "刷新连接器",
  "Refresh status": "刷新状态",
  "Refresh status to load this connection.": "刷新状态以加载此连接",
  "Refresh to load connector registrations.": "刷新以加载连接器注册信息",
  "Refreshing…": "刷新中…",
  "Register a connector": "注册连接器",
  "Register and start a connector from the Connectors tab first. The connector controls its local endpoint, credentials and process settings.":
    "请先在“连接器”页注册并启动连接器，本地接口、凭据和进程设置由连接器管理",
  "Register connector": "注册连接器",
  "Register one, install it on your private host, then add its advertised targets from Connections.":
    "注册连接器并安装到内网主机，然后在“服务连接”中添加其声明的目标",
  "Register the callback URL above with the provider, then enter its client credentials. Saving replaces any previous grant and requires authorization again.":
    "先在服务商处注册上方回调地址，再填写客户端凭据；保存将替换现有授权，需要重新完成授权",
  "Registered callback URL": "已注册的回调地址",
  "Registered schema hash": "已注册的接口定义哈希",
  "Registered version": "已注册版本",
  "Registering…": "正在注册…",
  "Registration creates a dedicated credential. The connector advertises its targets when it first connects.":
    "注册会创建专用凭据，连接器首次连接时将声明可用目标",
  "Registration token": "注册令牌",
  "Reload catalog history": "刷新目录历史",
  "Reload schedule": "刷新计划",
  "Reload servers": "刷新服务",
  "Reload servers to load your connections.": "刷新服务以加载连接信息",
  "Request only the permissions these tools need. Leave empty to use the provider default.":
    "仅申请工具所需的权限，留空使用服务商默认值",
  "Requested scopes": "申请的权限范围",
  Resource: "资源",
  "Response fields to keep": "要保留的响应字段",
  "Review and publish the draft in the tool registry.":
    "请在工具目录中审查并发布草稿",
  "Review what a server exposes": "查看服务提供的工具",
  "Revoke connector": "撤销连接器",
  "Revoked credentials cannot be re-enabled. Register a new connector to reconnect.":
    "已撤销的凭据无法重新启用，请注册新连接器以重新连接",
  "Revoking…": "正在撤销…",
  "Risk classification": "风险分类",
  "Run a connector inside your network to expose locally configured HTTP or stdio MCP targets.":
    "在内网运行连接器，接入本地配置的 HTTP 或 stdio MCP 目标",
  "Run connection check": "执行连接检查",
  "Save configuration": "保存配置",
  "Save schedule": "保存计划",
  "Save the registration token": "保存注册令牌",
  "Saving…": "保存中…",
  Scheduled: "定时",
  "Scheduled catalog checks": "定时目录检查",
  "Select a connection to discover its tool names, schemas, and import status.":
    "选择一个连接，查看其工具名称、参数定义和导入状态",
  "Server URL": "服务地址",
  "Server annotation:": "服务声明：",
  "Server details": "服务详情",
  "Server name": "服务名称",
  Servers: "服务",
  "Setup guide ↗": "接入指南 ↗",
  "Show token": "显示令牌",
  "Space-separated scopes": "以空格分隔权限范围",
  "Staging network": "测试环境网络",
  "Start the connector to register its configured targets":
    "启动连接器以注册已配置的目标",
  Status: "状态",
  Target: "目标",
  "Target fingerprint": "目标指纹",
  "The candidate initially keeps the tool’s risk and response policy. Review them against the changed behavior. The registered tool remains on its current version until publication.":
    "候选版本初始保留工具的风险与响应策略，请结合行为变更进行审查；发布前已注册工具保持当前版本",
  "The discovered definition differs from the registered tool. Create a candidate, then review its parameters, risk and response fields in the registry before publishing.":
    "发现的定义与已注册工具不同，请创建候选版本，在工具目录中审查参数、风险和响应字段后再发布",
  "The last check did not complete.": "上一次检查未完成",
  "This server is disabled. Enable it before connecting. Its previous grant has been invalidated.":
    "此服务已禁用，连接前请先启用；此前的授权已失效",
  "This server uses a credential reference. OAuth and static credentials cannot be combined. Add a connection without a credential reference to use OAuth.":
    "此服务使用凭据引用，OAuth 与静态凭据不能同时使用；如需 OAuth，请添加不含凭据引用的连接",
  "This token is shown once. Leaving this tab removes it from the console. Store it in the connector’s protected token file.":
    "令牌仅显示一次，离开此页面后将从控制台移除，请保存到连接器受保护的令牌文件",
  "Timeout (milliseconds)": "超时时间（毫秒）",
  "Token exchange": "令牌交换",
  "Token expires": "令牌到期时间",
  Tool: "工具",
  "Unique in this workspace. Used in every imported tool’s gateway name.":
    "在当前工作区内唯一，将用于所有导入工具的网关名称",
  "Upstream OAuth": "上游 OAuth",
  "Use the server’s Streamable HTTP endpoint. No query string or credentials.":
    "使用服务的 Streamable HTTP 地址，不包含查询参数或凭据",
  "Write — requires independent approval": "写入 — 默认需要独立审批",
  " · disabled": " · 已禁用",
  disabled: "已停用",
  enabled: "已启用",
  "may change external state": "可能改变外部状态",
  ms: "毫秒",
  "not provided": "未提供",
  optional: "选填",
  "read-only": "只读",
  "· HTTP and private targets": "· HTTP 与内网目标",
  "· Status from the last refresh": "· 状态来自最近一次刷新",
  Activity: "活动",
  Tools: "工具",
  Settings: "设置",
  "This server is disabled. Enable it to discover, import, or execute its tools.":
    "此服务已禁用，请启用后再发现、导入或执行工具",
  "Retry discovery": "重新发现",
  "Reading the server’s tool contracts…": "正在读取服务的工具契约…",
  "Only tools needing review": "仅显示需要审查的工具",
  "Discovered tools": "发现的工具",
  Imported: "已导入",
  "Review →": "审查 →",
  "No available tools need review.": "当前没有需要审查的可用工具",
  "This server currently exposes no tools.": "此服务当前未提供工具",
  "Missing from the upstream catalog": "上游目录中缺失的工具",
  "These registered tools were absent from this complete discovery. Review their dependencies and retire them in the registry if appropriate.":
    "本次完整发现未找到这些已注册工具，请检查依赖关系，并按需在工具目录中停用",
  "Review registered tool": "审查已注册工具",
  "Discover the available tools, then review each contract and its risk classification.":
    "发现可用工具后，请逐一审查契约与风险分类",
  Timeout: "超时时间",
  "Private target": "内网目标",
  "None configured": "未配置",
  "This target and its credentials are configured on the private connector host. Manage its connection from the Connectors tab.":
    "此目标及凭据在内网连接器主机上配置，请在“连接器”页管理连接",
  "Server access": "服务访问",
  "Disabling blocks discovery, imports and calls to this server.":
    "禁用后将阻止此服务的工具发现、导入和调用",
  "Updating…": "正在更新…",
  "Disable server": "禁用服务",
  "Enable server": "启用服务",
  "{count} tools discovered · Select one to review before importing.":
    "已发现 {count} 个工具 · 导入前请选择工具进行审查",
  "Checked {date}. Includes drafts and retired tools. Publication and access remain separate.":
    "检查时间：{date}，包含草稿和已停用工具；发布与访问权限分别管理",
  "{changed} changed · {missing} missing · {unimported} not imported":
    "{changed} 个已变更 · {missing} 个缺失 · {unimported} 个未导入",
  "Discovery started {date} · Review #{id}":
    "发现开始于 {date} · 审查记录 #{id}",
  "Registered v{version}": "已注册 v{version}",
  "{compatible} compatible · {incompatible} incompatible":
    "{compatible} 个兼容 · {incompatible} 个不兼容",
  "{count} consecutive failures · {date}. Previous catalog history is preserved.":
    "连续失败 {count} 次 · {date}，此前的目录历史仍保留",
  ". Review the tool’s behavior before choosing its risk classification.":
    "，请审查工具行为后再选择风险分类",
  "5 minutes to 24 hours. Repeated failures slow checks down, up to once a day. Disabling server access pauses checks automatically.":
    "间隔为 5 分钟至 24 小时，连续失败会延长检查间隔，最多每天一次；禁用服务访问后自动暂停检查",
  "The server list has been refreshed; check its current state before trying again.":
    "服务列表已刷新，请确认当前状态后重试",
  "Reload the schedule to check its saved state before trying again.":
    "请重新加载计划，确认已保存状态后重试",
  "Discover tools again before retrying; the previous import may have completed.":
    "上一次导入可能已完成，请重新发现工具后再重试",
  "Refresh status before trying again; the previous action may have completed.":
    "上一次操作可能已完成，请刷新状态后再重试",
  "Refresh the list before creating another connector; registration may have completed. If its token was lost, revoke it and create a new registration.":
    "注册可能已完成，请刷新列表后再创建连接器；若令牌已丢失，请撤销该连接器并重新注册",
  "Refresh the list to check whether revocation completed.":
    "请刷新列表，确认撤销是否已完成",
  "The server could not provide a complete catalog. Run connection diagnostics before checking again.":
    "服务未能提供完整目录，请运行连接诊断后重新检查",
  "The catalog check timed out.": "目录检查超时",
  "Server access changed while the check was running. Its result was discarded.":
    "检查期间服务访问配置发生变化，本次结果已丢弃",
  "The catalog could not be compared within the supported limits.":
    "目录超出支持的限制，无法完成比较",
  "Choose an interval between 5 minutes and 24 hours.":
    "请选择 5 分钟至 24 小时之间的间隔",
  "Schedule saved. The worker will check this server when due.":
    "计划已保存，后台任务将按计划检查此服务",
  "Scheduled checks paused.": "定时检查已暂停",
  Off: "已关闭",
  "Paused with server": "随服务暂停",
  Checking: "检查中",
  "Retry scheduled": "等待重试",
  "Not configured": "未配置",
  Disconnected: "未连接",
  "Awaiting authorization": "等待授权",
  "Finishing authorization": "正在完成授权",
  Connected: "已连接",
  "Refreshing tokens": "正在刷新令牌",
  "Reconnect required": "需要重新连接",
  "The gateway returned an invalid server list. Reload servers to try again.":
    "网关返回了无效服务列表，请重新加载服务后重试",
  "Clipboard unavailable. Select and copy the token manually.":
    "剪贴板不可用，请选中令牌并手动复制",
  "Token copied": "令牌已复制",
  "Copy unavailable. Select and copy the token manually.":
    "无法复制，请选中令牌并手动复制",
  "Not imported": "未导入",
  "In sync": "已同步",
  "Schema changed": "结构已变更",
  "Description changed": "描述已变更",
  "Missing upstream": "上游已移除",
  "Discover tools again before preparing a changed contract.":
    "请重新发现工具后再准备接口变更",
  "Provide a change reason of 1–1,000 UTF-8 bytes.":
    "请填写变更原因，长度为 1 至 1,000 个 UTF-8 字节",
  Revoked: "已撤销",
  "Awaiting connection": "等待连接",
  Online: "在线",
  Offline: "离线",
  "Connector name must contain 1–120 UTF-8 bytes and no control characters.":
    "连接器名称须为 1 至 120 个 UTF-8 字节，且不能含控制字符",
  "The gateway returned an invalid connector list. Refresh to try again.":
    "网关返回了无效连接器列表，请刷新后重试",
  "The gateway did not return a registration token.": "网关未返回注册令牌",
  "Revocation could not be confirmed.": "无法确认撤销是否完成",
  "OAuth endpoints must use HTTPS without embedded credentials or fragments.":
    "OAuth 端点必须使用 HTTPS，且不能含凭证或片段标识",
  "Enter the registered client ID, using at most 2,048 UTF-8 bytes and no control characters.":
    "请输入已注册的客户端 ID，最多 2,048 个 UTF-8 字节，且不能含控制字符",
  "Choose a client authentication method supported by this provider.":
    "请选择此提供方支持的客户端认证方式",
  "Enter the registered client secret, using at most 8,192 UTF-8 bytes and no line breaks.":
    "请输入已注册的客户端密钥，最多 8,192 个 UTF-8 字节，且不能含换行",
  "Use at most 64 space-separated OAuth scopes of up to 256 ASCII characters each, without quotes or backslashes.":
    "最多填写 64 个以空格分隔的 OAuth 权限范围，每个最多 256 个 ASCII 字符，且不能含引号或反斜杠",
  "The gateway returned an invalid OAuth status. Refresh status before continuing.":
    "网关返回了无效 OAuth 状态，请刷新状态后继续",
  "The gateway returned an incomplete OAuth configuration. Refresh status before continuing.":
    "网关返回的 OAuth 配置不完整，请刷新状态后继续",
  "The gateway returned an invalid token expiry. Refresh status before continuing.":
    "网关返回的令牌过期时间无效，请刷新状态后继续",
  "The gateway returned incomplete provider metadata. Discover the provider again.":
    "网关返回的提供方元数据不完整，请重新发现提供方",
  "Refresh OAuth status before taking another action.":
    "请刷新 OAuth 状态后再执行操作",
  "Configuration saved. Connect to authorize this server.":
    "配置已保存，请连接并授权此服务",
  "Save an OAuth configuration before connecting.": "请先保存 OAuth 配置再连接",
  "The gateway returned an invalid authorization link.":
    "网关返回了无效授权链接",
  "Opening the provider’s authorization page…": "正在打开提供方的授权页面…",
  "Stored tokens cleared. Provider-side consent is unchanged.":
    "已清除存储的令牌，提供方的授权记录保持不变",
  "Server name must contain 1–120 UTF-8 bytes and no line breaks.":
    "服务名称须为 1 至 120 个 UTF-8 字节，且不能含换行",
  "Namespace must start with a lowercase letter and use at most 24 lowercase letters, digits, underscores, or hyphens.":
    "命名空间须以小写字母开头，最多 24 位，仅允许小写字母、数字、下划线和连字符",
  "Timeout must be an integer between 100 and 120,000 milliseconds.":
    "超时时间须为 100 至 120,000 毫秒之间的整数",
  "Choose an enabled private connector. Refresh its status if needed.":
    "请选择已启用的私有连接器，必要时刷新其状态",
  "Choose a target advertised by this connector.": "请选择此连接器声明的目标",
  "Enter a complete HTTP or HTTPS server URL.":
    "请输入完整的 HTTP 或 HTTPS 服务地址",
  "Server URL must use HTTP or HTTPS, contain no query or fragment, and fit within 2,048 UTF-8 bytes.":
    "服务地址必须使用 HTTP 或 HTTPS，不能含查询参数或片段标识，最多 2,048 个 UTF-8 字节",
  "Use a credential reference instead of credentials in the URL.":
    "请使用凭证引用，不要将凭证放在 URL 中",
  "Credential references use uppercase letters, digits, and underscores, starting with a letter.":
    "凭证引用须以字母开头，仅允许大写字母、数字和下划线",
  "Response limit must be an integer between 1,024 and 131,072 bytes.":
    "响应上限须为 1,024 至 131,072 字节之间的整数",
  "Keep at most 32 response field paths.": "最多保留 32 个响应字段路径",
  "Each response field must be a JSON Pointer beginning with /, with valid ~0 or ~1 escapes, and at most 256 UTF-8 bytes.":
    "每个响应字段必须是以 / 开头的 JSON Pointer，使用有效的 ~0 或 ~1 转义，最多 256 个 UTF-8 字节",
  "Response field paths must not contain empty segments or embedded wildcards. Use a complete * segment to select a field from each array element.":
    "响应字段路径不能含空段或嵌入式通配符，请使用完整的 * 段选取各数组元素中的字段",
  "An array selector * must be followed by a field path. Select the array field itself to keep each complete element.":
    "数组选择符 * 后须跟字段路径；如需保留完整元素，请直接选择数组字段",
  "Response field paths must not repeat or overlap a parent and child field.":
    "响应字段路径不能重复，也不能同时选择父字段和子字段",
  "A response node cannot mix an array selector * with specific object keys.":
    "同一响应节点不能混用数组选择符 * 与具体对象键",
  "Only administrators can manage MCP servers.": "只有管理员可以管理 MCP 服务",
  "Enable this MCP server before discovering or importing tools.":
    "请先启用此 MCP 服务再发现或导入工具",
  "The gateway returned an invalid discovery result. Discover tools again.":
    "网关返回了无效发现结果，请重新发现工具",
  "The gateway returned an invalid catalog comparison. Discover tools again.":
    "网关返回了无效目录比较结果，请重新发现工具",
  "The server returned duplicate tool names. Review its configuration before importing.":
    "服务返回了重复工具名称，请检查配置后再导入",
  "Discovery did not complete. Previous catalog reviews remain available; run connection diagnostics before checking again.":
    "发现未完成，先前的目录审查仍可查看，请运行连接诊断后重试",
  "Discover tools again and review the current tool schema before importing.":
    "请重新发现工具并审查当前结构后再导入",
  "This tool has already been imported. Open it in the tool registry.":
    "此工具已导入，请在工具目录中打开",
  "Discover tools again before retrying. The previous import may have completed.":
    "上一次导入可能已完成，请重新发现工具后再重试",
  "Choose a read or write risk classification.": "请选择读取或写入风险分类",
  "Import returned an unexpected tool. Discover tools again to check the registry.":
    "导入返回的工具不符合预期，请重新发现工具并检查目录",
  "Definition is compatible; business execution has not been tested.":
    "定义兼容，尚未验证业务执行",
  "Connector connection and catalog checks passed; no business tool was executed.":
    "连接器连接与目录检查通过，未执行业务工具",
  "Enable the server before checking its connection.": "请先启用服务再检查连接",
  "Connector discovery failed; check its status, target configuration and catalog compatibility.":
    "连接器发现失败，请检查状态、目标配置及目录兼容性",
  "The endpoint, timeout or credential is blocked by the configured policy.":
    "端点、超时设置或凭证被当前策略阻止",
  "The upstream server rejected authentication.": "上游服务拒绝了身份验证",
  "Connection or MCP initialization failed; inspect endpoint, network and protocol compatibility.":
    "连接或 MCP 初始化失败，请检查端点、网络及协议兼容性",
  "The upstream catalog could not be completely read.": "无法完整读取上游目录",
  "The catalog changed while it was being read; run discovery again before review.":
    "读取期间目录发生变化，请重新发现后再审查",
  "The upstream catalog response is unsupported.": "不支持此上游目录响应",
  "The upstream catalog exceeds the supported size.": "上游目录超过支持的大小",
  "The upstream catalog contains unsupported JSON.":
    "上游目录包含不支持的 JSON",
  "The upstream catalog response is malformed.": "上游目录响应格式错误",
  "The upstream catalog exceeds the supported tool count.":
    "上游目录超过支持的工具数量",
  "Tool name is unsupported.": "工具名称不受支持",
  "The catalog repeats this tool name.": "目录中存在重复工具名称",
  "Tool description exceeds the supported size.": "工具描述超过支持的大小",
  "Input schema is unsupported or exceeds the schema limit.":
    "输入结构不受支持或超过结构限制",
  "Output schema is unsupported or exceeds the schema limit.":
    "输出结构不受支持或超过结构限制",
  "Discovery completed with incompatible definitions; strict import and execution remain blocked.":
    "发现已完成，但存在不兼容定义，严格导入与执行仍被阻止",
  "Connection and catalog checks passed; no business tool was executed.":
    "连接与目录检查通过，未执行业务工具",
  "The upstream catalog repeated a pagination cursor.":
    "上游目录返回了重复分页游标",
  "The upstream catalog exceeds the supported page count.":
    "上游目录超过支持的页数",
  "{count} connection": "{count} 个连接",
  "{count} connections": "{count} 个连接",
};
