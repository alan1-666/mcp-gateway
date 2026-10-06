export const toolsZh: Record<string, string> = {
  "Reload current data": "重新加载当前数据",
  "Loading current records…": "正在加载当前记录…",
  "New API key": "新访问密钥",
  "Save this API key now": "请立即保存此访问密钥",
  "Close and discard": "关闭并清除",
  "This key is shown once. Closing this panel or leaving this page removes it from the console.":
    "此密钥仅显示一次，关闭面板或离开页面后将从控制台清除",
  "Copy key": "复制密钥",
  "{count} records loaded": "已加载 {count} 条记录",
  "Loading…": "加载中…",
  "Load more": "加载更多",
  "Invocation approval policy": "调用审批策略",
  "Invocation policy": "调用策略",
  "Choose whether this tool requires an independent approval for each operation. Client permissions always apply.":
    "选择此工具的每次操作是否需要独立审批，客户端权限始终生效",
  "Approval policy": "审批策略",
  "Require independent approval": "要求独立审批",
  "Allow authorized calls without approval": "允许已授权调用直接执行",
  "This allows authorized clients to perform this tool's writes without per-call approval. Existing pending approvals are retained.":
    "此设置允许已授权客户端直接执行写操作，无需逐次审批，现有待审批操作保持不变",
  "Saving…": "保存中…",
  "Publish policy change": "发布策略变更",
  "Apply filters": "应用筛选",
  "Audit records": "审计记录",
  "{count} matching records": "共 {count} 条匹配记录",
  "No audit records match these filters.": "没有符合筛选条件的审计记录",
  Actor: "操作主体",
  Resource: "资源",
  "Recorded change": "记录的变更",
  "Outcome verification": "结果核查",
  "Record evidence from the downstream system. The original operation remains UNKNOWN; verification does not execute or retry it.":
    "记录来自下游系统的证据，原操作仍保持 UNKNOWN 状态，核查不会执行或重试操作",
  "No verification evidence recorded.": "尚未记录核查证据",
  "Observed outcome": "观察到的结果",
  Inconclusive: "尚无定论",
  "Confirmed success": "确认成功",
  "Confirmed failure": "确认失败",
  "Evidence reference": "证据引用",
  "HTTPS URL or record ID": "HTTPS 链接或记录 ID",
  "HTTPS links must omit query parameters and fragments.":
    "HTTPS 链接不能包含查询参数或片段",
  "Verification notes": "核查说明",
  "Recording…": "记录中…",
  "Record verification evidence": "记录核查证据",
  "An independent administrator or approver must verify this outcome.":
    "此结果必须由独立的管理员或审批人核查",
  "Edit response policy": "编辑响应策略",
  "MCP RESPONSE POLICY": "MCP 响应策略",
  "Choose the result fields agents receive": "选择 Agent 接收的结果字段",
  "Changes apply to newly prepared operations. Existing operations keep the policy in their original snapshot.":
    "变更适用于新准备的操作，已有操作继续使用原快照中的策略",
  "Reloading…": "重新加载中…",
  "Reload latest contract": "加载最新接口定义",
  "Your draft is retained. Reloading does not save it.":
    "草稿已保留，重新加载不会保存草稿",
  "Saved policy · version {version}": "已保存的策略 · 版本 {version}",
  "Response fields to keep": "要保留的响应字段",
  optional: "选填",
  "One field path per line, up to 32. Leave blank to retain all fields. Use /results/*/title to keep title from each array element, or /results to keep the whole array.":
    "每行填写一个字段路径，最多 32 个，留空保留全部字段；使用 /results/*/title 保留每个数组元素的 title，或使用 /results 保留整个数组",
  "Maximum response size (bytes)": "最大响应大小（字节）",
  "1,024–131,072 bytes inline. Oversized results are rejected unless bounded large-result storage is enabled.":
    "直接返回的大小为 1,024–131,072 字节；未启用有界大结果存储时，超限结果会被拒绝",
  "Enable bounded large-result storage": "启用有界大结果存储",
  "Maximum large result (bytes)": "大结果上限（字节）",
  "Retention (seconds)": "保留时长（秒）",
  "Only validated, filtered structured MCP results are stored. Large results return an operation reference; authorized clients use read_result to retrieve chunks before expiry. No public download link is created.":
    "仅存储经过校验和过滤的结构化 MCP 结果；大结果返回操作引用，授权客户端可在到期前使用 read_result 分块读取，不会生成公开下载链接",
  "Array selection keeps the original element order and count. Every element must contain each selected field, otherwise the result is rejected. Numeric path segments select object keys; array indices are not supported.":
    "数组筛选保留原有元素顺序和数量，每个元素都必须包含所选字段，否则结果会被拒绝；数字路径段表示对象键，不支持数组下标",
  "Top-level nextCursor and next_cursor are kept when present. Include other pagination fields explicitly. Text is rebuilt from the retained result.":
    "存在时保留顶层 nextCursor 和 next_cursor，其他分页字段需明确选择；文本内容由保留的结果重新生成",
  "Sample MCP response (JSON)": "MCP 响应样例（JSON）",
  "The initial JSON is an example, not a response from this tool. Paste a raw MCP CallToolResult envelope with content and, when available, structuredContent. The preview request must fit within 256 KiB.":
    "初始 JSON 仅为示例，并非此工具的实际响应；请粘贴包含 content 及可用 structuredContent 的原始 MCP CallToolResult，预览请求不得超过 256 KiB",
  "Previewing…": "预览中…",
  "Preview sample": "预览样例",
  "Save response policy": "保存响应策略",
  "Preview processes only the sample. It does not call the upstream server or save a policy.":
    "预览仅处理样例，不调用上游服务或保存策略",
  "Response policy preview": "响应策略预览",
  "Original sample": "原始样例",
  bytes: "字节",
  "Projected result": "裁剪后结果",
  "Preview result · tool v{version}": "预览结果 · 工具 v{version}",
  "Projected MCP result": "裁剪后的 MCP 结果",
  "Read large result": "读取大结果",
  "Large result stored with an expiry. Load only the chunks you need; each request checks current access.":
    "大结果已限时存储，可按需加载分块，每次请求都会检查当前访问权限",
  "Read next 8 KiB": "读取下一块 8 KiB",
  "Complete result loaded · {bytes} bytes": "完整结果已加载 · {bytes} 字节",
  Transport: "传输方式",
  "MCP server ID": "MCP 服务 ID",
  "Remote tool": "远程工具",
  Destination: "目标地址",
  Timeout: "超时时间",
  "Credential reference": "凭证引用",
  "None configured": "未配置",
  "Response policy": "响应策略",
  "Only these fields are retained:": "仅保留以下字段：",
  "All result fields are retained.": "保留结果的所有字段",
  "Large results: up to {bytes} bytes; retained for {seconds} seconds.":
    "大结果上限为 {bytes} 字节，保留 {seconds} 秒",
  "Inline maximum {bytes} bytes. Top-level nextCursor and next_cursor are preserved when present.":
    "直接返回上限为 {bytes} 字节，存在时保留顶层 nextCursor 和 next_cursor",
  "REVIEWED RELEASES": "经审查的发布",
  "Versions & candidates": "版本与候选版本",
  "Current v{version}": "当前 v{version}",
  "Publishing creates a new immutable definition. Prepared operations retain their original snapshot. Rollback creates a candidate for review.":
    "发布会创建新的不可变定义，已准备的操作保留原快照；回滚会创建一个待审查的候选版本",
  "Change reason": "变更原因",
  "Upstream server": "上游服务",
  " (disabled)": "（已禁用）",
  "Upstream tool name": "上游工具名称",
  "Risk for refreshed upstream contract": "更新后接口的风险类型",
  Read: "只读",
  "Write — approval required": "写入 — 需要审批",
  "Proposed HTTP definition": "拟更新的 HTTP 定义",
  "Working…": "处理中…",
  "Probe upstream and create candidate": "检查上游并创建候选版本",
  "Create candidate for review": "创建待审查的候选版本",
  "Saved candidates": "已保存的候选版本",
  "Based on v{version}": "基于 v{version}",
  "Published as v{version}": "已发布为 v{version}",
  "Awaiting review": "等待审查",
  "No candidates yet.": "暂无候选版本",
  "Review candidate changes": "审查候选变更",
  "Field changes": "字段变更",
  "Complete proposed definition": "完整的拟更新定义",
  "Proposed contract": "拟更新的接口定义",
  "This candidate is stale. Create a new candidate from the current version.":
    "此候选版本已过期，请基于当前版本创建新的候选版本",
  "Publish reviewed candidate": "发布已审查的候选版本",
  "Discard candidate": "丢弃候选版本",
  "Version history": "版本历史",
  "Version {version}": "版本 {version}",
  "Immutable definition": "不可变定义",
  "Create rollback candidate from v{version}": "从 v{version} 创建回滚候选版本",
  "No version history available.": "暂无版本历史",
  "Load older versions": "加载更早版本",
  "Retire tool": "停用工具",
  "Retirement removes this tool from discovery and stops new dispatches. Restoration requires publishing a reviewed candidate.":
    "停用后此工具将从发现列表移除并停止新的执行请求，恢复需要发布经审查的候选版本",
  "Confirm retirement": "确认停用",
  "This tool is retired. A reviewed candidate can restore it.":
    "此工具已停用，可通过发布经审查的候选版本恢复",
  "Agent runtime status": "Agent 运行环境状态",
  "Showing the last reported status.": "当前显示最近一次上报的状态",
  "Configured model": "已配置模型",
  "Worker last seen": "Worker 最近在线时间",
  " The task list may be out of date.": " 任务列表可能已过期",
  "Workspace administrators can review all workspace tasks.":
    "工作空间管理员可以查看空间内的所有任务",
  "Only your own tasks are shown. Workspace administrators can review them.":
    "仅显示你自己的任务，工作空间管理员也可查看这些任务",
  "Return to pending request": "返回待处理请求",
  "New Agent task": "新建 Agent 任务",
  "Agent task history": "Agent 任务历史",
  "Task history": "任务历史",
  "Latest 100": "最近 100 条",
  "Loading task history…": "正在加载任务历史…",
  "Task history could not be loaded. Use Refresh to try again.":
    "无法加载任务历史，请刷新重试",
  "No tasks recorded": "暂无任务记录",
  "Describe a task to start a governed Agent run. Its prompt, output, and activity stay together.":
    "描述任务以启动受网关管理的 Agent 执行，指令、输出与活动将统一记录",
  "Your role cannot create Agent tasks. An administrator or operator can start a task.":
    "当前角色无法创建 Agent 任务，管理员或操作员可以创建任务",
  "A TASK WITH A TRACEABLE OUTCOME": "结果可追踪的任务",
  "What needs to be done?": "需要完成什么？",
  "The Agent can discover and call published workspace tools. Writes remain subject to the gateway's approval policy.":
    "Agent 可发现并调用工作空间内已发布的工具，写操作仍受网关审批策略约束",
  " The result of this request may be uncertain. Retry sends the same prompt and request ID.":
    " 此请求结果可能不确定，重试将发送相同指令和请求 ID",
  "Task instructions": "任务指令",
  "Describe the outcome, relevant service, and constraints. The Agent will use only the tools available in this workspace.":
    "描述目标结果、相关服务和约束条件，Agent 仅使用此工作空间中可用的工具",
  "Request locked for safe retry. The prompt will remain immutable.":
    "请求已锁定以支持安全重试，指令保持不变",
  "The prompt is immutable once submitted.": "指令提交后不可修改",
  "/ 8,000 characters": "/ 8,000 字符",
  "Creation request ID": "创建请求 ID",
  "An unfinished request is saved in this browser tab for recovery after a refresh. Signing out clears the draft.":
    "未完成的请求保存在此浏览器标签页中，以便刷新后恢复；退出登录会清除草稿",
  "This browser could not save the recovery draft. Keep this page open while a request result is uncertain.":
    "浏览器无法保存恢复草稿，请在请求结果不确定时保持此页面打开",
  "The runtime needs attention. You can queue this task; it will wait for a worker or model credentials.":
    "运行环境需要处理，你可以先将任务加入队列，等待 Worker 或模型凭证就绪",
  "Creating task…": "创建任务中…",
  "Retry same request": "重试同一请求",
  "Create Agent task": "创建 Agent 任务",
  "Back to tasks": "返回任务列表",
  "AGENT WORKSPACE": "Agent 工作空间",
  "One task. Its complete record.": "一个任务，一份完整记录",
  "Select a task to inspect its immutable instructions, live output, governed operations, and event history.":
    "选择任务以查看原始指令、实时输出、受控操作和事件历史",
  "Describe an outcome": "描述目标结果",
  "The Agent works with the published tools your account may use.":
    "Agent 使用你的账号有权访问的已发布工具",
  "Review sensitive actions": "审查敏感操作",
  "Write requests pause for independent human approval.":
    "写请求会暂停并等待独立人工审批",
  "Check the evidence": "检查执行证据",
  "Task completion and business operation outcomes have separate records.":
    "任务完成状态与业务操作结果分别记录",
  "Loading task and event history…": "正在加载任务与事件历史…",
  "Refresh task": "刷新任务",
  "AGENT TASK RECORD": "Agent 任务记录",
  "Task details": "任务详情",
  "The record may be out of date.": "此记录可能已过期",
  "Refresh the task before trying another action.":
    "请刷新任务后再尝试其他操作",
  "Refreshing…": "刷新中…",
  "Created by": "创建人",
  Attempt: "执行次数",
  Created: "创建时间",
  Updated: "更新时间",
  "Recorded reason:": "记录的原因：",
  "Original instructions": "原始指令",
  Immutable: "不可修改",
  "RELATED TOOL OPERATION": "关联的工具操作",
  "The external action may have completed. Verify its downstream state before taking further action. Resume is blocked while the operation is uncertain.":
    "外部操作可能已完成，请核查下游状态后再采取行动；操作结果不确定时无法继续任务",
  "An admitted action is still in progress. Its operation record determines the eventual outcome.":
    "已接受的操作仍在执行，最终结果以操作记录为准",
  "An independent authorized reviewer must approve the exact tool arguments. Resume the task after approval.":
    "需要独立且有权限的审批人确认本次工具参数，审批通过后可继续任务",
  "Inspect the tool's recorded outcome before continuing the task.":
    "继续任务前请检查工具记录的执行结果",
  "The related operation could not be loaded:": "无法加载关联操作：",
  "Inspect operation": "查看操作",
  "Open approval inbox →": "打开审批列表 →",
  "Resuming…": "正在继续…",
  "Resume task": "继续任务",
  "Cancel task": "取消任务",
  "Resume is unavailable until the related operation is approved, completed, or safely resolved.":
    "关联操作获批、完成或安全核查后，才能继续任务",
  "Cancel this task?": "取消此任务？",
  "Cancellation stops new tool actions. A downstream action already admitted may still finish; cancellation does not undo it.":
    "取消会停止新的工具操作，已接受的下游操作仍可能完成，取消不会撤销这些操作",
  "Cancelling…": "取消中…",
  "Confirm cancellation": "确认取消",
  "Keep task": "保留任务",
  "Agent output": "Agent 输出",
  "Follow output": "跟随输出",
  "Catching up with the recorded event stream…": "正在同步已记录的事件流…",
  "The output is long. This view shows its most recent 262,144 characters.":
    "输出较长，此视图显示最近的 262,144 个字符",
  "Waiting for text from this attempt…": "等待本次执行输出文本…",
  "No Agent output has been recorded for this task state.":
    "当前任务状态下尚未记录 Agent 输出",
  "A successful Agent task means the task finished. Check each tool operation to confirm whether a requested business action succeeded.":
    "Agent 任务成功表示任务执行结束，业务动作是否成功请逐项检查工具操作记录",
  "Task activity": "任务活动",
  "Showing the latest {count} loaded events. Output is reconstructed across the complete loaded stream.":
    "显示最近加载的 {count} 条事件，输出根据已加载的完整事件流重建",
  "Text added to Agent output": "已向 Agent 输出追加文本",
  "Event details · #{id}": "事件详情 · #{id}",
  "Event #{id}": "事件 #{id}",
  "No task events returned yet.": "尚未返回任务事件",
  "Clipboard unavailable in this browser. Use a secure HTTPS connection.":
    "此浏览器无法使用剪贴板，请使用安全的 HTTPS 连接",
  "Copied to clipboard.": "已复制到剪贴板",
  "Copy unavailable. Select and copy the key manually.":
    "无法自动复制，请选中密钥后手动复制",
  "Not reported": "未上报",
  "Checking Agent runtime": "正在检查 Agent 运行环境",
  "Runtime status unavailable": "无法获取运行环境状态",
  "Worker offline": "Worker 离线",
  "Model account needs attention": "模型账号需要处理",
  "Runtime ready": "运行环境已就绪",
  "Checking the workspace's configured worker and model connection.":
    "正在检查工作空间配置的 Worker 和模型连接",
  "Tasks can be queued. An administrator needs to bring the workspace worker online before they can run.":
    "任务可以加入队列，管理员需要先启动工作空间的 Worker 才能执行",
  "An administrator must connect the worker's model account. Tasks may wait for credentials; resume them after the account is available.":
    "管理员需要连接 Worker 的模型账号，任务可能等待凭证，账号可用后可继续任务",
  "The configured worker reports a model connection. Tasks run with governed tools and may pause for approval.":
    "已配置的 Worker 已上报模型连接，任务使用受控工具执行，并可能暂停等待审批",
  "The gateway returned an event for a different task.":
    "网关返回了其他任务的事件",
  "Waiting for the workspace worker. The task will start when a worker can claim it.":
    "正在等待工作空间的 Worker，领取任务后将开始执行",
  "The Agent is working through the task. Published tools remain subject to gateway access and approval checks.":
    "Agent 正在执行任务，已发布工具仍受网关访问权限和审批检查约束",
  "The related operation has been approved. Resume this task to continue with the recorded operation.":
    "关联操作已获批，继续此任务即可执行已记录的操作",
  "The task is paused for a tool operation to be reviewed. Inspect the exact request, then resume after approval.":
    "任务已暂停以等待工具操作审查，请检查具体请求并在获批后继续",
  "The model account is unavailable. An administrator must restore the worker's model connection; then resume this same task.":
    "模型账号不可用，管理员需恢复 Worker 的模型连接后，再继续同一个任务",
  "Execution paused for review. Inspect recorded operations before resuming; an interrupted connection does not prove a downstream action failed.":
    "执行已暂停等待核查，继续前请检查操作记录；连接中断不代表下游操作失败",
  "The Agent task finished. Its output and tool operation records remain available for review.":
    "Agent 任务已结束，仍可查看输出和工具操作记录",
  "The task stopped with a recorded failure. Review its output and related operation before starting another task.":
    "任务因已记录的错误停止，启动新任务前请检查输出及关联操作",
  "The task was cancelled. Already admitted downstream actions may still finish; inspect their operation records.":
    "任务已取消，已接受的下游操作仍可能完成，请检查其操作记录",
  "The candidate detail is incomplete. Reload and inspect it again before publishing.":
    "候选版本详情不完整，请重新加载并检查后再发布",
  "Provide a reason before creating a candidate.": "创建候选版本前请填写原因",
  "Tool definition": "工具定义",
  "Sample response": "响应样例",
  "The preview request exceeds 256 KiB. Use a smaller representative sample.":
    "预览请求超过 256 KiB，请使用更小的代表性样例",
  "Sample response contains a number that would be rounded or underflow during submission. Use a representative value the browser can preserve, or a string when the response contract allows it.":
    "响应样例包含提交时会被舍入或下溢的数字，请使用浏览器能准确保留的代表性数值，或在响应契约允许时使用字符串",
  "Only administrators can edit or preview an MCP tool’s response policy.":
    "仅管理员可以编辑或预览 MCP 工具的响应策略",
  "Reload the latest contract and review your retained draft before continuing. No changes have been retried.":
    "继续前请加载最新接口定义并检查保留的草稿，尚未重试任何变更",
  "This tool changed since you opened it. Your inputs are retained. Reload the latest contract and review them before saving; this request will not be retried automatically.":
    "此工具在打开后发生了变更，输入已保留；请加载最新接口定义并检查后再保存，此请求不会自动重试",
  "The gateway returned an invalid preview. Preview the sample again.":
    "网关返回了无效预览，请重新预览样例",
  "The update returned an unexpected tool version.":
    "更新返回了非预期的工具版本",
  "The tool is no longer available as an MCP integration.":
    "此工具已无法作为 MCP 集成使用",
  "The gateway returned an invalid or changed result chunk. Restart reading the result.":
    "网关返回的结果分块无效或已变更，请重新读取结果",
  "The result chunk has an inconsistent size or continuation cursor.":
    "结果分块大小或后续游标不一致",
  "The gateway returned an invalid event cursor.": "网关返回了无效的事件游标",
  "Describe your task in 1–8,000 characters.": "请使用 1–8,000 个字符描述任务",
  "Choose whether large-result storage is enabled.": "请选择是否启用大结果存储",
  "Large-result limit must be an integer between the inline limit and 1,048,576 bytes.":
    "大结果上限必须是直接返回上限至 1,048,576 字节之间的整数",
  "Large-result retention must be an integer between 60 and 86,400 seconds.":
    "大结果保留时长必须是 60–86,400 秒之间的整数",
  "Enter a valid date and time.": "请输入有效的日期和时间",
  "Each header needs a name and secret value.":
    "每个请求头都需要填写名称和密钥值",
  "Header names must be unique, ignoring letter case.":
    "请求头名称不能重复，不区分大小写",
  "Provide between 1 and 16 authentication headers.":
    "请提供 1–16 个认证请求头",
  "actor id": "主体 ID",
  "resource id": "资源 ID",
  action: "操作",
  from: "开始时间",
  to: "结束时间",
  inconclusive: "尚无定论",
  "confirmed success": "确认成功",
  "confirmed failure": "确认失败",
  queued: "排队中",
  running: "执行中",
  "waiting approval": "等待审批",
  "waiting credentials": "等待凭证",
  "needs review": "需要核查",
  succeeded: "已成功",
  failed: "已失败",
  cancelled: "已取消",
  ready: "已就绪",
  dispatching: "执行中",
  unknown: "结果未知",
  rejected: "已拒绝",
  "Policy already matches version {version}; no version was added.":
    "策略已与版本 {version} 一致，未新增版本",
  "Saved as version {version}. New operations use this policy; already prepared operations keep their existing snapshot.":
    "已保存为版本 {version}，新操作使用此策略，已准备的操作保留原快照",
  "Loaded version {version}. Your draft and sample are retained; review them against the saved policy before saving.":
    "已加载版本 {version}，草稿和样例已保留，请对照已保存策略检查后再保存",
  "This tool is now at version {version}. Reload the latest contract and review your retained draft before saving.":
    "此工具当前为版本 {version}，保存前请加载最新接口定义并检查保留的草稿",
  " The update may have completed. Reload the latest contract before saving again.":
    " 更新可能已完成，请加载最新接口定义后再保存",
};
