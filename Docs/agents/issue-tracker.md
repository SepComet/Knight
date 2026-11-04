# Issue 追踪：本地 Markdown

本仓库的 issue 与规格以 Markdown 文件存放在 `.scratch/` 下。

## 约定

- 一个特性一个目录：`.scratch/<feature-slug>/`
- 规格文件：`.scratch/<feature-slug>/spec.md`
- 实现 ticket 一文件一票：`.scratch/<feature-slug>/issues/<NN>-<slug>.md`，从 `01` 编号，绝不合并成单个 tickets 文件
- 每个 issue 文件顶部附近用 `Status:` 行记录状态
- 评论与对话历史追加到文件末尾 `## Comments` 标题下

## 技能要求“发布到 issue tracker”时

在 `.scratch/<feature-slug>/` 下新建文件（目录不存在则创建）。

## 技能要求“读取相关 ticket”时

直接读取所引用路径的文件；用户通常会直接给出路径或 issue 编号。

## 寻路操作（供 `/wayfinder` 使用）

- **Map**：`.scratch/<effort>/map.md`
- **子 ticket**：`.scratch/<effort>/issues/NN-<slug>.md`，从 `01` 编号，正文写问题。
  `Type:` 行记录类型（`research`/`prototype`/`grilling`/`task`）；`Status:` 行记录 `claimed`/`resolved`。
- **阻塞关系**：顶部附近 `Blocked by: NN, NN`；所列文件全部 `resolved` 才解除阻塞。
- **前沿（Frontier）**：扫描该目录中未解决、未阻塞、未认领的文件，编号最小者优先。
- **认领**：先写 `Status: claimed` 并保存，再开工。
- **解决**：在 `## Answer` 标题下追加答案，置 `Status: resolved`，并把要点+链接追加到 `map.md` 的 Decisions-so-far。
