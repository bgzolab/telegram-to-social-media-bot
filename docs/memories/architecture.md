---
title: 项目架构
created: 2026-04-30T00:00:00
modified: 2026-05-04T14:30:00
description: 架构需要对项目的整体结构进行说明，明确每个文件夹和文件的作用，以及它们之间的关系。需要列出每个文件的作用，以及它们之间的调用关系。如果有数据库，包含完整数据库结构。
tags:
  - ai-notes
---

# 项目架构

## 运行链路

1. `main.go` 启动程序并装配依赖。
2. `bootstrapservice` 加载配置、初始化日志和数据库。
3. `pipelineservice` 统一编排处理顺序。
4. `archiveservice` 负责归档和入库。
5. `syncservice` 负责判定是否同步并调用平台适配器。
6. `notifyservice` 负责生成通知消息。
7. Telegram bot 把通知发回目标聊天。

## 模块边界

### 入口层

- `main.go`：CLI 启动、依赖装配、bot 启动。
- 入口层不写业务规则。
- 当前 bot 入口已注册 `/admin` 命令与 `admin:` callback 路由。

### 服务层

- `internal/service/bootstrapservice`：运行前初始化。
- `internal/service/pipelineservice`：主流程编排。
- `internal/service/archiveservice`：消息归档、模板渲染、文件落盘、数据库写入。
- `internal/service/jsonbackfillservice`：离线扫描 `archives/json` 并把历史 JSON 补录进 SQLite。
- `internal/service/syncservice`：同步判定、平台分发。
- `internal/service/notifyservice`：通知目标解析、通知内容拼装。
- `internal/service/adminqueryservice`：管理首页、来源列表、消息列表、消息详情等只读查询编排。


### 数据层

- `internal/Entity`：配置和核心数据结构。
- `internal/Database`：SQLite 连接和持久化。
- 数据库唯一约束是重复消息的第一防线。
- 社媒同步结果已独立建模为同步记录表，按消息、平台、尝试次数保留历史。

### 适配层

- `pkg/SocialMediaUtils`：Twitter、Mastodon、BlueSky 适配。
- `pkg/TgUtils`：Telegram 辅助逻辑。
- `pkg/StrUtils`：文本处理辅助逻辑。
- 工具层尽量保持纯逻辑，不混入流程编排。

## 目录职责

- `config/`：唯一配置文件和模板文件。
- `archives/`：Markdown 归档输出。
- `json/`：原始 JSON 输出。
- `docs/memories/`：只保留给 LLM 的稳定上下文。
- `docs/implementation-plans/`：实施计划，不属于长期上下文。
- `scripts/`：迁移或辅助脚本。

## 数据流

### 归档流

- Telegram 消息进入 `archiveservice`。
- 服务解析 `bucket`、`source_id`、`message_id`。
- 服务基于消息正文构建归档模板数据，并渲染完整 Markdown 文件。
- 归档文件名由归档层统一计算，默认可回退为 `message_id.md`，也可由配置模板稳定生成。
- 当前新格式去重先按目标归档文件是否存在判断；旧单文件格式仍保留兼容回退读取。
- 生成归档文件后再写数据库。
- 成功或失败结果交给通知层。

### 同步流

- `syncservice` 先判断总开关。
- 再判断频道是否精确命中目标列表。
- 命中后构建"文本 + 可选图片"载荷，再按平台适配器分发。
- 相册（`media_group_id` 非空）消息不即时分发：先由 `albumservice` 登记分组，静默窗口到期后聚合整组投递一次。
- 图片数量超过平台单帖上限（4 张）时，按回复线程续发剩余图片。
- 平台适配器优先尝试图文发送；图失败时回退到纯文字发送。
- 每个平台的结果都返回给通知层。
- 默认同步阶段会在平台分发后立即写入同步记录；相册聚合投递会为组内每个成员各写一份结果记录。
- 同步记录当前固定保存：平台状态、远端链接或远端标识、错误信息、图片使用情况、触发方式、尝试次数。
- 平台分发前当前会先执行统一文本预处理，并按平台上限截断。
- 平台分发结果当前统一返回结构化对象，供通知层和持久化层消费。

### 相册聚合同步流

- Telegram 相册的成员消息是独立 update，但共享 `message.media_group_id`（同一 chat 内唯一）。
- 归档阶段把 `media_group_id` 持久化到 `messages.media_group_id`，非相册消息为空。
- `albumservice` 按 `(来源, media_group_id)` 维护静默窗口定时器（默认 15 秒，可用
  `socialMediaSync.albumDebounceSeconds` 覆盖），新成员会顺延窗口；收满 10 条（Telegram 上限）立即投递。
- 窗口到期后从数据库读取整组成员，正文取组内所有非空正文按源消息 ID 顺序拼接，图片按成员顺序收集。
- 投递前会再次检查分组是否已有**成功且由自动投递产生**的同步记录：已有则跳过，
  避免迟到成员或窗口内并发重发；失败记录与手动重同步记录不阻止自动投递，保证失败相册可重试。
- 全部平台投递失败、或分发结果为空时，按静默窗口重试，最多 3 次；失败记录写入后可被重启恢复兜底。
- 分组投递结果写入组内每个成员的同步记录，通知只发一条，包含来源链接与图片数量。
- 进程启动时 `RecoverPending` 会恢复最近 30 分钟内归档、且尚无同步记录的分组；
  更早的分组不会自动补投，只能通过管理页手动重同步。
- 历史数据可用 `tg migrate media-groups` 从 `archives/json` 回填 `media_group_id`（只填空值，不触发同步）。

### 管理交互流

- Telegram bot 第一版管理交互固定采用按钮回调流。
- 入口层负责接收按钮事件并路由到管理查询或单次操作。
- 管理查询流固定为：首页统计 -> 来源列表 -> 消息列表 -> 消息详情。
- 重同步动作只允许从消息详情触发，不在首页、来源列表、消息列表直接执行。
- 管理交互不改变主处理链路，仍与 `archive -> sync -> notify` 并存。
- 查询服务当前已固定提供：首页统计、来源列表、按来源查看消息、消息详情、同步异常筛选。
- 当前页面跳转主要通过编辑同一条 bot 消息完成。

### 手动重同步流

- 手动重同步当前由 Telegram 管理页按钮触发。
- 入口层只负责接收按钮事件并调用 `syncservice` 的手动重同步能力。
- 执行前先校验消息来源是否仍命中可同步频道。
- 执行中对“同一消息 + 同一范围”应用最小防重入保护。
- 执行后结果立即追加写入同步记录，并回显到 Telegram 管理页。
- 最终回归口径当前以 `go test ./...` 为准，核心管理链路已纳入自动测试覆盖。

### R2 附件上传流

- R2 上传在 archive 阶段独立执行，不影响 sync 与 notify。
- `Attachment.S3Url` 是新列，Markdown 导出优先使用 R2 URL，回落本地路径。
- 历史附件通过 `tg migrate attachments-to-r2` 迁移。
- 本地图片文件始终保留，不做删除。
- 附件文件名保留原始扩展名（file_unique_id + 远程扩展名），R2 object key 不带扩展名的历史对象需修复后重传。

### JSON 补录流

- `tg migrate json-to-db -c ./config/config.yaml` 负责离线历史补录。
- 补录流固定读取 `archives/json` 下的历史 Telegram Update JSON。
- 补录流复用 `archiveservice.ResolveSourceMeta()`、`archiveservice.SelectMsgText()` 和 `archiveservice.BuildArchivedMessage()`。
- 补录流只写数据库，不重写 Markdown，不下载附件，不触发社媒同步。
- 幂等口径固定依赖数据库唯一约束 `(message_id, username)`。

## 当前稳定约束

- 主流程顺序固定为 `archive -> sync -> notify`。
- 默认执行模式是 `serial`。
- `async_experimental` 只能在 pipeline 内部切换，不能改变外部接口。
- 新平台必须通过 provider 或 adapter 扩展，不能把平台细节写回主流程。
- 归档完整内容输出边界固定为 `config/template.txt`，服务层只负责准备模板数据。
- 归档文件命名策略固定由 `archiveservice.ResolveArchiveFileName()` 统一收口。
- 当前图片同步范围固定为单张图片。
- 第一版 bot 管理入口固定为 4 层视图，不引入命令参数驱动跳转。
- 第一版重同步粒度固定为“单个平台”或“当前消息全部平台”。
- 同步记录历史保留策略固定为追加写入，不做覆盖更新。

## 已知限制

- 历史 `notification` 配置仍可能存在于旧配置里，但当前实现不消费。
- 社交平台错误处理仍偏粗，只到成功或失败级别。
- memories 只记录稳定事实，不再记录阶段进度和迁移过程。
   - 读取：先新后旧；
   - 迁移：以 DB 为主，一次性全量补齐本地；
   - 删除：旧单文件必须在 zip 备份完成后再删除。
3. 迁移期错误传播：`source_id` 约束失败等错误必须可被通知阶段消费并展示具体原因。
4. 双向核对口径已冻结为 `(chat_id, message_id)`。
