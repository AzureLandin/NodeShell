# NodeShell Agent 供应商模型 ID 编辑器美化：设计与实施计划

- **报告日期**：2026-08-26
- **报告状态**：实施完成，全量自动化测试通过
- **目标界面**：设置 -> Agent -> 供应商 -> 模型
- **推荐方案**：将“每行一个模型 ID”的普通 textarea 改为模型标签编辑器
- **兼容原则**：后端协议继续使用 `models: string[]`，不修改设置文件结构和 Wails API
- **本次交付**：代码修改与自动化测试已全部完成

---

## 1. 当前问题

Agent 供应商配置中的模型列表当前使用一个三行 textarea：

```tsx
<textarea
  value={draft.modelsText}
  rows={3}
  onChange={(e) => patchDraft(draft.key, { modelsText: e.target.value })}
/>
<p className="settings-hint">每行一个模型 ID。</p>
```

保存时再将文本按换行拆分：

```ts
models: draft.modelsText
  .split(/\r?\n/)
  .map((s) => s.trim())
  .filter(Boolean)
```

这能完成数据录入，但交互和视觉上存在明显不足：

1. 已添加模型没有清晰的独立视觉边界，扫描多个 ID 较困难；
2. 删除某个模型需要在文本中手动选择并修改；
3. 重复模型只有保存后才由后端规范化，前端没有即时反馈；
4. 不容易看到当前模型数量以及 32 个模型的后端上限；
5. 粘贴逗号分隔、空格分隔或多行模型时缺少明确处理；
6. textarea 与设置页其他精细化控件相比显得临时和简陋；
7. 对键盘用户而言，编辑单个条目不如标签式编辑直观。

## 2. 当前数据契约

### 2.1 前端草稿

当前 `ProviderDraft` 使用：

```ts
modelsText: string
```

从后端状态加载时执行：

```ts
modelsText: provider.models.join('\n')
```

### 2.2 Wails API

保存供应商的 API 已经使用结构化数组：

```ts
interface AgentProviderInput {
  id?: string
  name: string
  baseUrl: string
  models: string[]
}
```

因此 UI 不需要继续以 textarea 字符串作为内部主状态。

### 2.3 Go 后端规范化

主要文件：

```text
internal/settings/settings.go
app.go
```

后端行为：

- 每个供应商最多保留 `32` 个模型；
- 每个模型 ID 最长 `512` 字节；
- 自动去除首尾空白；
- 自动忽略空字符串；
- 按原顺序去重；
- 创建第一个供应商时，第一个模型可能成为默认模型。

本次美化不应改变这些规则，只应在前端更早地表达同样的约束。

## 3. 推荐交互设计

### 3.1 总体形态

将 textarea 替换为一个带边框的模型编辑区域：

```text
模型
┌────────────────────────────────────────────┐
│ [gpt-4o-mini  ×] [gpt-4.1  ×]             │
│ 添加模型 ID…                         [＋]  │
└────────────────────────────────────────────┘
支持 Enter、逗号或粘贴多个 ID        2 / 32
```

组成部分：

1. 已添加模型的标签列表；
2. 单行模型 ID 输入框；
3. 使用加号图标的添加按钮；
4. 每个标签使用关闭图标删除；
5. 底部辅助说明；
6. 当前数量与上限计数。

不建议把每个标签做成过度圆润、彩色的大胶囊。设置界面属于工作型工具，应采用紧凑、低饱和、清晰边界的样式。

### 3.2 添加模型

输入框支持：

- `Enter` 添加当前内容；
- 英文逗号 `,` 添加当前内容；
- 失去焦点时，如果内容有效则提交；
- 点击加号按钮添加；
- 粘贴多行或逗号分隔内容时批量添加。

建议解析分隔符：

```ts
/[,\r\n]+/
```

不建议默认使用普通空格分割，因为部分自定义模型 ID 理论上可能包含空格，而后端当前只要求非空且不超过长度上限。

### 3.3 去重与顺序

前端添加时：

1. 对每项执行 `trim()`；
2. 忽略空项；
3. 按精确字符串比较去重；
4. 保留首次添加顺序；
5. 已存在的模型不重复添加。

大小写不应自动转换。模型 ID 可能由 provider 定义，前端不能擅自 lower-case。

### 3.4 删除模型

每个标签右侧提供关闭图标按钮：

```text
[deepseek-chat ×]
```

要求：

- 使用按钮而不是可点击 span；
- `aria-label="删除模型 deepseek-chat"`；
- hover/focus 时提供明确反馈；
- 删除后焦点落到合理位置，不能丢到 document body；
- busy/未加载完成时禁用删除。

### 3.5 键盘操作

建议支持：

| 按键 | 行为 |
|---|---|
| `Enter` | 添加输入框中的模型 |
| `,` | 添加输入框中的模型，不把逗号写入 ID |
| `Backspace` | 输入框为空时删除最后一个模型 |
| `Escape` | 清空当前未提交输入，不关闭 Provider 卡片 |
| `Tab` | 正常移动到标签删除按钮、输入框和添加按钮 |

中文输入法组合态期间不得把 Enter 当作提交：

```ts
if (event.nativeEvent.isComposing) return
```

### 3.6 批量粘贴

粘贴以下内容：

```text
gpt-4o-mini
gpt-4.1, o3-mini
deepseek-chat
```

应一次解析成多个标签。建议：

- 如果剪贴板包含逗号或换行，则阻止默认粘贴并批量添加；
- 如果只是普通单个 ID，则正常写入输入框，用户可继续编辑；
- 重复项静默忽略或用短暂非阻塞提示说明；
- 超过 32 项时只保留前 32 项，并显示明确提示。

### 3.7 空状态

没有模型时，不要显示一个大片空白框。输入区域内可以展示紧凑 placeholder：

```text
例如 gpt-4o-mini、deepseek-chat、qwen-plus
```

不应硬编码为可点击的供应商推荐模型，以免模型列表过时或误导用户。示例只用于说明格式。

## 4. 推荐组件设计

### 4.1 新建独立组件

建议新增：

```text
src/renderer/src/components/ModelIdEditor.tsx
```

推荐 props：

```ts
interface ModelIdEditorProps {
  value: string[]
  onChange: (models: string[]) => void
  disabled?: boolean
  maxItems?: number
  maxItemLength?: number
  label: string
  placeholder: string
}
```

默认值应与后端一致：

```ts
maxItems = 32
maxItemLength = 512
```

如果不希望在前端重复硬编码后端常量，可以在组件附近定义有明确注释的 UI 常量，并通过测试确保与 Go 端一致。当前 Wails API 没有暴露这些 metadata，不建议为了一个固定上限增加新 binding。

### 4.2 将草稿状态改为数组

推荐将：

```ts
type ProviderDraft = {
  modelsText: string
}
```

改为：

```ts
type ProviderDraft = {
  models: string[]
}
```

对应修改：

```ts
draftsFromStatus
applyStatusToDrafts
addProvider
save
```

保存时直接传：

```ts
models: draft.models
```

这样 UI 状态和 API 类型一致，不再需要反复 `join('\n')` / `split(/\r?\n/)`。

### 4.3 是否保留 `modelsText`

不推荐继续将 textarea 字符串作为唯一状态再派生标签，因为：

- 删除和去重逻辑会变复杂；
- 分隔符格式会不断变化；
- 标签与文本源容易不同步；
- 保存前仍需二次解析。

标签编辑器内部可以有一个局部 `input` 字符串，但已提交模型应始终存为 `string[]`。

## 5. 视觉设计建议

### 5.1 容器

建议 class：

```text
model-id-editor
model-id-editor-list
model-id-editor-input-row
model-id-editor-input
model-id-editor-add
model-id-editor-meta
```

容器：

- 使用现有 `--bg-input`；
- 1px `--glass-border`；
- `var(--radius-control)`，不使用大圆角；
- focus-within 使用现有 `--accent-focus-ring`；
- 最小高度稳定，标签换行时自然增长；
- 最大高度约 150–180px，超出后内部滚动，避免 Provider 卡片无限增高。

### 5.2 模型标签

建议标签：

- 高度约 26–28px；
- 4px 或最多 6px 圆角；
- 使用 `--bg-elevated` / `--border-subtle`；
- 模型 ID 使用等宽字体：`Consolas, 'Cascadia Mono', monospace`；
- 文本可选择，便于复制；
- 单个超长 ID 使用 `max-width`、ellipsis 和 title；
- 删除按钮使用熟悉的关闭图标并提供 tooltip/aria-label。

不要使用随机颜色区分模型。颜色没有业务含义，还会造成深浅主题一致性问题。

### 5.3 输入和添加按钮

输入框：

- 与标签在同一容器中；
- 宽度可伸缩；
- 不重复绘制第二层厚边框；
- 使用模型 ID 示例 placeholder；
- 达到 32 项时禁用并显示“已达到上限”。

添加按钮：

- 使用 Font Awesome 或项目已有图标库中的加号；
- 图标按钮尺寸固定；
- `aria-label="添加模型"`；
- 输入为空、重复、过长或达到上限时禁用；
- 不使用写有“添加”的大文本按钮占据整行。

### 5.4 辅助信息

底部左右布局：

```text
Enter 或逗号添加，可粘贴多个 ID                 3 / 32
```

发生校验错误时，左侧提示切换为错误文本：

- “该模型已存在”；
- “模型 ID 不能超过 512 个字符”；
- “每个供应商最多添加 32 个模型”。

错误应使用现有 `form-inline-error` 风格或新增同级样式，不使用 Toast，因为错误只属于当前控件。

## 6. 响应式与布局

设置弹窗宽度约 560px，Provider 内容区更窄。模型编辑器必须：

- 标签自动换行；
- 最长单词不会撑破容器；
- 输入行在窄宽度下保持至少可输入；
- 数量计数不覆盖辅助文字；
- 在小屏下 meta 行允许换行；
- 删除按钮尺寸稳定，不随长 ID 收缩；
- 不出现横向滚动整个设置弹窗。

建议 CSS：

```css
.model-id-chip {
  min-width: 0;
  max-width: 100%;
}

.model-id-chip-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
```

## 7. 国际化文案

需要修改：

```text
src/renderer/src/i18n/locales/zh.json
src/renderer/src/i18n/locales/en.json
```

建议新增 key：

```text
agentModelPlaceholder
agentModelAdd
agentModelRemove
agentModelsMeta
agentModelsDuplicate
agentModelsTooLong
agentModelsLimit
agentModelsEmpty
```

中文建议：

```text
模型 ID
例如 gpt-4o-mini
添加模型
删除模型 {{model}}
按 Enter 或逗号添加，可粘贴多个模型 ID
该模型已存在
模型 ID 不能超过 512 个字符
每个供应商最多添加 32 个模型
至少添加一个模型 ID
```

英文建议：

```text
Model IDs
For example, gpt-4o-mini
Add model
Remove model {{model}}
Press Enter or comma to add; paste multiple model IDs
This model already exists
Model IDs cannot exceed 512 characters
Up to 32 models per provider
Add at least one model ID
```

现有 `agentModelsHint: One model id per line` 应替换或停止使用。

## 8. 校验策略

### 8.1 前端即时校验

建议在添加时验证：

```text
trim 后非空
长度 <= 512
当前数组中不存在
总数 < 32
```

### 8.2 保存校验

当前后端允许空模型数组，但没有模型的供应商无法真正配置 Agent。产品层面建议保存按钮在 `models.length === 0` 时禁用，并显示“至少添加一个模型 ID”。

这是行为上的轻微收紧。实施前如果必须完全保持现有行为，可以允许保存空数组，但 UI 应明确显示该供应商不可用于发送 prompt。推荐采用至少一个模型的要求，因为当前 `agentConfigFor` 只能选择 provider 已列出的模型。

### 8.3 后端仍为最终事实来源

不能因为前端有校验就删除 Go 端：

```text
normalizeModelID
normalizeModelList
AgentModelsMax
```

后端必须继续防御旧版本前端、手工配置和异常 binding 输入。

## 9. 建议修改文件

### 必改

```text
src/renderer/src/components/SettingsModal.tsx
src/renderer/src/components/ModelIdEditor.tsx
src/renderer/src/App.css
src/renderer/src/i18n/locales/zh.json
src/renderer/src/i18n/locales/en.json
tests/ui/settings.test.tsx
```

### 通常无需修改

```text
app.go
internal/settings/settings.go
src/shared/types.ts
src/renderer/src/api/adapter.ts
```

原因：API 和存储本来就是 `models: string[]`，本次主要是前端编辑体验升级。

## 10. 实施步骤

### Phase 1：抽取模型编辑器

1. 新建 `ModelIdEditor.tsx`；
2. 实现标签、输入框、添加和删除；
3. 实现 Enter、逗号、Backspace 和组合输入处理；
4. 实现批量粘贴解析；
5. 实现去重、长度和数量校验；
6. 仅通过 `value/onChange` 管理已提交模型。

### Phase 2：接入 Provider 草稿

1. 将 `ProviderDraft.modelsText` 改为 `models: string[]`；
2. 修改 status -> draft 映射；
3. 修改保存参数；
4. 修改新 Provider 默认值；
5. 保持保存后卡片收起、API Key 清空等现有行为。

### Phase 3：样式和国际化

1. 添加紧凑的 editor/chip/input/icon button 样式；
2. 验证深色和浅色主题；
3. 添加中英文文案；
4. 验证长模型 ID 和窄窗口。

### Phase 4：测试与视觉验收

1. 更新原 textarea 测试；
2. 新增模型标签编辑器单元测试；
3. 运行前端完整测试和构建；
4. 在 Wails 中进行桌面和窄窗口截图验收。

## 11. 自动化测试计划

### 11.1 已有 Provider 保存测试迁移

当前测试通过：

```ts
screen.getByLabelText('Models 1')
user.clear(...)
user.type(...)
```

改为：

1. 删除原模型标签；
2. 在模型输入框输入新 ID；
3. Enter 添加；
4. 保存；
5. 断言 `upsertProvider` 收到正确的 `models: string[]`。

### 11.2 添加与删除

测试：

- Enter 添加模型；
- 点击加号添加；
- 点击删除图标移除指定模型；
- 空输入不会添加；
- 删除最后一个后显示空状态/校验状态。

### 11.3 键盘行为

测试：

- 逗号提交；
- 空输入时 Backspace 删除最后一个；
- 非空输入 Backspace 只编辑文本；
- Escape 清空草稿输入；
- `isComposing` 时 Enter 不提交。

### 11.4 批量粘贴

测试粘贴：

```text
gpt-4o-mini\ngpt-4.1,o3-mini
```

断言生成三个标签，并保持顺序。

### 11.5 去重与上限

- 重复 ID 不增加数量；
- 前后空白被去除；
- 大小写不同的 ID 仍视为不同值；
- 第 33 项被拒绝并显示上限提示；
- 超过 512 字符的 ID 被拒绝；
- 数量计数正确。

### 11.6 状态同步

- `agent.status()` 返回的模型变成对应标签；
- 保存响应后 draft 使用规范化后的模型；
- Provider 收起再展开，模型仍在；
- busy 时输入、删除和添加均禁用；
- Provider 删除后 editor 状态随卡片销毁。

### 11.7 验证命令

```powershell
npm run typecheck
npm run lint
npm test -- --run tests/ui/settings.test.tsx
npm test
npm run build:wails
```

## 12. 验收清单

- [x] 添加供应商后模型区域第一眼可理解；
- [x] 输入模型 ID 后按 Enter 立即形成标签；
- [x] 粘贴多行模型 ID 能批量生成标签；
- [x] 标签可逐个删除；
- [x] 长模型 ID 不撑破设置弹窗；
- [x] 32 个模型时布局仍可使用，并明确显示上限；
- [x] 深色和浅色主题下边框、文本和 focus ring 清楚；
- [x] 窄窗口下标签、输入框、按钮和计数不重叠；
- [x] 保存后 Agent 模型选择菜单展示正确；
- [x] 编辑已有供应商时原模型全部正确加载；
- [x] API Key、供应商名称、Base URL 和删除操作无回归；
- [x] 键盘和屏幕阅读器可以识别添加、删除及当前数量。

## 13. 风险与处理

### 风险 1：修改草稿结构影响已有 Provider 流程

`modelsText -> models` 会影响多个映射函数。必须一次性修改：

```text
draftsFromStatus
applyStatusToDrafts
addProvider
save
相关测试
```

避免同时保留两个字段作为双重事实来源。

### 风险 2：批量粘贴误拆模型 ID

只按逗号和换行拆分，不按 `/`、`:`、`.`、`-` 或普通空格拆分。这些字符经常属于合法模型 ID。

### 风险 3：标签过多导致 Provider 卡片过高

模型列表应设置合理最大高度并内部滚动；计数和输入行保持可见。不能让 32 个模型把整个设置页拉得过长。

### 风险 4：条件收起导致输入草稿丢失

已提交模型由 Provider draft 保存，不受折叠影响；尚未按 Enter 提交的局部 input 可能在组件卸载时丢失。当前 Provider collapse 使用 `display: none` 而非卸载，因此通常不会丢失。若后续动画简化改为条件渲染，需要决定是否在收起前自动提交或将 input 草稿提升到 Provider 状态。

### 风险 5：前端限制与后端常量漂移

当前后端上限为 32/512。建议在测试或注释中明确来源；若后端未来修改常量，应同步更新前端。

## 14. 不推荐的方案

### 14.1 只给 textarea 加阴影或渐变

只能改变外观，不能解决删除、去重、计数和批量输入问题。

### 14.2 为每个模型增加单独的大输入框

32 个模型会产生冗长表单和大量视觉噪声，重复操作效率低。

### 14.3 使用下拉菜单列出固定模型

供应商是 OpenAI-compatible 自定义 endpoint，模型 ID 不可能由 NodeShell 完整预知。固定下拉会过时，并阻止私有或本地模型。

### 14.4 自动请求 `/models` 获取列表作为唯一输入方式

不同供应商的 `/models` 兼容性、鉴权和响应格式并不稳定。未来可作为“获取模型”辅助按钮，但不能替代手工模型 ID 编辑器，也不应纳入本次美化范围。

### 14.5 彩色标签和大圆角胶囊

模型 ID 没有天然颜色分类。随机颜色会干扰扫描，且不符合当前设置界面的安静工具风格。

## 15. 交给实施 Agent 的最短任务说明

请将 Agent 供应商中的模型 textarea 改为紧凑的模型 ID 标签编辑器：

1. 新建受控 `ModelIdEditor`，value 为 `string[]`；
2. 标签展示现有模型，关闭图标删除；
3. 单行输入支持 Enter、逗号、加号按钮和批量粘贴；
4. 支持空输入 Backspace 删除最后一项，并正确处理 IME composing；
5. 前端即时 trim、去重，限制 32 项和每项 512 字符；
6. 将 `ProviderDraft.modelsText` 改为 `models: string[]`；
7. 保存 API 保持 `models: string[]`，不改 Go 后端和 settings schema；
8. 使用紧凑低饱和样式、等宽模型文字、稳定图标按钮和数量计数；
9. 添加中英文文案和 settings UI 测试；
10. 验证深浅主题、窄窗口和长模型 ID。

## 16. 最终建议

本需求最合适的实现不是“美化 textarea”，而是将模型列表提升为与其数据结构匹配的标签编辑器：

```text
string textarea
  ->
结构化模型标签 + 单行添加 + 批量粘贴 + 即时校验
```

该方案能显著改善视觉质量和编辑效率，同时不修改 Wails API、Go 存储、Keyring 或 Agent 运行逻辑，改动边界清晰，适合独立实施和验收。
