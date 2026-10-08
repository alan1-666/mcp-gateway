/** UI-owned OpenAPI import messages; API names and schema data stay unchanged. */
export const openapiZh: Record<string, string> = {
  "OpenAPI reference expansion exceeds the document budget.":
    "OpenAPI 引用展开超出整份文档的预算",
  "Server URL and operation path must fit within 4096 bytes.":
    "服务地址与接口路径合计不能超过 4096 字节",
  "Close import": "关闭导入",
  "Import OpenAPI": "导入 OpenAPI",
  "This imported operation requires a managed credential reference.":
    "此导入操作需要配置受管理的凭证引用",
  "Imported settings are a draft. Review the destination, credential, schema, and risk before creating it. Approval is required by default.":
    "导入配置仅为草稿，请在创建前审查目标、凭证、结构及风险；默认需要审批",
  "Import review notes": "导入审查提示",
  "Required by API": "API 要求",
  "Import from OpenAPI": "从 OpenAPI 导入",
  "Turn an API operation into a tool": "将 API 操作转换为工具",
  "Cancel import": "取消导入",
  "Upload or paste an OpenAPI document, inspect its operations, then review one tool draft. Preview stays in this browser and makes no network requests.":
    "上传或粘贴 OpenAPI 文档，查看接口操作，再审查一个工具草稿；预览仅在当前浏览器中处理，不会发送网络请求",
  "Supported profile: OpenAPI 3.0 or 3.1 JSON, static paths, GET with scalar query parameters, or other supported methods with a JSON object body. Unsupported operations are listed with a reason; nothing is imported automatically.":
    "支持范围：OpenAPI 3.0 或 3.1 JSON、静态路径、使用标量查询参数的 GET，或使用 JSON 对象请求体的其他受支持方法；不支持的操作会列出原因，不会自动导入",
  "Upload JSON file": "上传 JSON 文件",
  "Maximum file size: 1 MiB. YAML and remote document URLs are not supported.":
    "文件最大 1 MiB，不支持 YAML 或远程文档地址",
  "OpenAPI JSON": "OpenAPI JSON",
  "Paste the complete OpenAPI JSON document": "粘贴完整的 OpenAPI JSON 文档",
  "Server URL override (optional)": "覆盖服务地址（选填）",
  "Use an absolute HTTP or HTTPS base URL when the document has no usable server URL. This changes only the preview destination; no connection is made.":
    "若文档中没有可用的服务地址，可填写绝对 HTTP 或 HTTPS 基础地址；此项仅改变预览目标，不会建立连接",
  "OpenAPI documents must not exceed 1 MiB.": "OpenAPI 文档不能超过 1 MiB",
  "The selected file could not be read. Choose it again or paste JSON.":
    "无法读取所选文件，请重新选择或粘贴 JSON",
  "The OpenAPI document could not be inspected.": "无法检查此 OpenAPI 文档",
  "Reading file…": "正在读取文件…",
  "Preview operations": "预览接口操作",
  "Reading the selected file locally…": "正在本地读取所选文件…",
  "Operation preview": "接口操作预览",
  "Choose one operation": "选择一个接口操作",
  "{supported} supported · {unsupported} unsupported":
    "支持 {supported} 个 · 不支持 {unsupported} 个",
  "No operations were found in this document.": "文档中未发现接口操作",
  "Document operations": "文档中的接口操作",
  Supported: "支持",
  Unsupported: "不支持",
  "This operation is outside the supported import profile.":
    "此操作超出支持的导入范围",
  "Selected operation": "已选接口操作",
  "Review the generated tool": "审查生成的工具",
  "No description provided by this document.": "文档未提供描述",
  "The document declares authentication. Configure a credential reference when reviewing the draft.":
    "文档声明了身份认证要求，请在审查草稿时配置凭证引用",
  "No authentication declared. Verify the API requirements before publishing.":
    "文档未声明身份认证，请在发布前核实 API 要求",
  "Review notes": "审查提示",
  "Generated input schema": "生成的输入结构",
  "Review draft opens the normal tool form. Confirm the name, destination, risk, credentials, and schema there before creating a draft. Publication and client access remain separate.":
    "点击“审查草稿”进入工具表单，确认名称、目标、风险、凭证及结构后再创建草稿；发布与客户端授权需分别配置",
  "Review draft": "审查草稿",
  "Expected an OpenAPI object.": "此处应为 OpenAPI 对象",
  "OpenAPI document exceeds 1 MiB.": "OpenAPI 文档超过 1 MiB",
  "OpenAPI document must be valid JSON.": "OpenAPI 文档必须是有效的 JSON",
  "OpenAPI document contains too many values.": "OpenAPI 文档包含的值过多",
  "OpenAPI document exceeds the nesting limit.": "OpenAPI 文档超过嵌套深度限制",
  "OpenAPI document contains an invalid null character.":
    "OpenAPI 文档包含无效的空字符",
  "OpenAPI document contains an unsafe property name.":
    "OpenAPI 文档包含不安全的属性名称",
  "OpenAPI document contains duplicate object keys.":
    "OpenAPI 文档包含重复的对象键",
  "OpenAPI document contains a number the browser cannot preserve exactly.":
    "OpenAPI 文档包含浏览器无法精确保留的数字",
  "Only local JSON Pointer references are supported.":
    "仅支持文档内的 JSON Pointer 引用",
  "References with sibling fields are not supported.":
    "不支持带有同级字段的引用",
  "Cyclic or deeply nested references are not supported.":
    "不支持循环引用或过深的嵌套引用",
  "Invalid local reference.": "文档内引用无效",
  "Local reference target was not found.": "未找到文档内引用目标",
  "Expanded input schema exceeds the complexity limit.":
    "展开后的输入结构超过复杂度限制",
  "Input schema uses unsupported keywords or a custom dialect.":
    "输入结构使用了不支持的关键字或自定义方言",
  "Input schema must declare a supported single type.":
    "输入结构必须声明一个受支持的单一类型",
  "Read-only and write-only schema semantics require manual mapping.":
    "只读和只写结构语义需要手动映射",
  "Schema annotations have invalid types.": "结构注解的类型无效",
  "Input schema format requires manual validation.": "输入结构格式需要手动验证",
  "Unsupported nullable schema syntax.": "不支持此可空结构语法",
  "Invalid OpenAPI 3.0 exclusive bound.": "OpenAPI 3.0 的排他边界无效",
  "Invalid JSON Schema exclusive bound.": "JSON Schema 的排他边界无效",
  "Input schema has an invalid numeric constraint.":
    "输入结构包含无效的数字约束",
  "Input schema has an invalid pattern.": "输入结构包含无效的正则表达式",
  "Input schema pattern requires manual validation.":
    "输入结构的正则表达式需要手动验证",
  "Input schema has an invalid enum.": "输入结构包含无效的枚举",
  "Object constraints require an object schema.": "对象约束必须用于对象结构",
  "Input schema has invalid required properties.": "输入结构的必填属性无效",
  "Array constraints require an array schema.": "数组约束必须用于数组结构",
  "Expanded input schema exceeds 64 KiB.": "展开后的输入结构超过 64 KiB",
  "Select an absolute HTTP(S) server URL without credentials, query, fragment or variables.":
    "请选择绝对 HTTP(S) 服务地址，且不能包含凭证、查询参数、片段标识或变量",
  "Invalid server URL.": "服务地址无效",
  "Server URLs must not contain credentials.": "服务地址不能包含凭证",
  "Only static absolute paths without parameters, query or fragment are supported.":
    "仅支持不含参数、查询或片段标识的静态绝对路径",
  "Invalid path encoding.": "路径编码无效",
  "Encoded separators, dot segments and dynamic paths require manual mapping.":
    "编码后的分隔符、点路径段及动态路径需要手动映射",
  "Security requirements must be an array.": "安全要求必须是数组",
  "Alternative security requirements require manual mapping.":
    "可选的多套安全要求需要手动映射",
  "Security scopes require manual mapping.": "安全权限范围需要手动映射",
  "Security scheme was not found.": "未找到安全方案",
  "Only header API keys and HTTP basic or bearer authentication can be mapped.":
    "仅可映射请求头 API 密钥及 HTTP Basic 或 Bearer 认证",
  "Authentication requires conflicting or reserved headers.":
    "身份认证要求使用冲突或保留的请求头",
  "A documented JSON success response is required.":
    "文档必须声明 JSON 成功响应",
  "Empty or wildcard success responses require manual mapping.":
    "空响应或使用通配符的成功响应需要手动映射",
  "Every success response must declare only application/json content.":
    "每个成功响应必须仅声明 application/json 内容",
  "Parameters must be a bounded array.": "参数必须是数量不超限的数组",
  "Invalid parameter name or location.": "参数名称或位置无效",
  "Duplicate parameters require manual review.": "重复参数需要手动审查",
  "This HTTP method is not supported by the gateway adapter.":
    "网关适配器不支持此 HTTP 方法",
  "Callbacks and webhooks require manual mapping.":
    "回调与 Webhook 需要手动映射",
  "Output schemas are not imported. Review the response contract before publishing.":
    "不会导入输出结构，请在发布前审查响应契约",
  "Imported tools are write-risk drafts requiring approval. Review the risk and permissions before publishing.":
    "导入工具默认为需要审批的写入风险草稿，请在发布前审查风险及权限",
  "Select an absolute HTTP(S) server URL.": "请选择绝对 HTTP(S) 服务地址",
  "The first declared server was selected. Review the destination before importing.":
    "已选择文档中声明的第一个服务，请在导入前审查目标地址",
  "Configure an operator-managed credential for the exact destination origin. No authentication secrets are imported.":
    "请为确切的目标源配置管理员管理的凭证，不会导入任何认证密钥",
  "GET request bodies are not supported.": "不支持 GET 请求体",
  "Only scalar query parameters with form serialization are supported.":
    "仅支持采用 form 序列化的标量查询参数",
  "Parameter required must be a boolean.": "参数的 required 字段必须是布尔值",
  "Query arrays, objects and nullable values require manual mapping.":
    "查询参数中的数组、对象及可空值需要手动映射",
  "Body requests cannot include query, path, header or cookie parameters.":
    "带请求体的操作不能同时包含查询、路径、请求头或 Cookie 参数",
  "This adapter always sends a JSON object body for non-GET requests.":
    "此适配器始终为非 GET 请求发送 JSON 对象请求体",
  "Optional request bodies require manual mapping because this adapter always sends a body.":
    "此适配器始终发送请求体，可选请求体需要手动映射",
  "Request bodies must declare only application/json content.":
    "请求体必须仅声明 application/json 内容",
  "Custom body encoding requires manual mapping.":
    "自定义请求体编码需要手动映射",
  "Request body schema must require a non-null JSON object.":
    "请求体结构必须要求非空的 JSON 对象",
  "Schema defaults are annotations and are not injected into requests. Gateway egress policy still applies.":
    "结构默认值仅作为注解，不会注入请求；网关出站策略仍然生效",
  "Operation description exceeds 4000 bytes.": "操作描述超过 4000 字节",
  "Only OpenAPI 3.0.x and 3.1.x JSON documents are supported.":
    "仅支持 OpenAPI 3.0.x 和 3.1.x JSON 文档",
  "Custom OpenAPI schema dialects are not supported.":
    "不支持自定义 OpenAPI 结构方言",
  "Path item references must resolve to local objects without cycles or siblings.":
    "路径项引用必须指向文档内的对象，且不能存在循环或同级字段",
  "OpenAPI document exceeds 256 operations.": "OpenAPI 文档超过 256 个操作",
  "Duplicate generated tool names require manual review.":
    "生成的工具名称重复，需要手动审查",
};
