# 领域文档

探索代码库前，工程技能应如何消费本仓库的领域文档。

## 探索前先读

- 仓库根 `GLOSSARY.md`
- `docs/adr/`：读与本次工作区域相关的 ADR

文件不存在就静默跳过，不要提示缺失，也不要主动建议先创建。`/grill-with-docs`、`/improve-codebase-architecture` 中的 `/domain-modeling` 会在术语或决策真正落地时惰性创建。

## 文件结构（单上下文）

```
/
├── GLOSSARY.md
├── docs/adr/
│   ├── 0001-xxx.md
└── Knight-Client/  Knight-Server/  Knight-Tools/  Knight-Design/
```

## 使用术语表词汇

输出中出现领域概念（issue 标题、重构提案、假设、测试名）时，使用 `GLOSSARY.md` 的定义，不要漂移到它明确回避的同义词。概念不在其中是个信号：要么你在发明项目不用的语言，要么存在真实缺口（记下来交给 `/domain-modeling`）。

## 标出 ADR 冲突

若你的输出与既有 ADR 矛盾，显式指出，而不是默默覆盖：

> _与 ADR-0007 矛盾，但值得重开，因为……_
