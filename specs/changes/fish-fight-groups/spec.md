# 补全鱼种搏斗习性组参数

Status: in-progress

## Problem Statement

`fight27StaticsFor` 原来只有官方逐鱼种表（少数鱼种）和 `marine` / `cyprinid`
两个习性组；其余两百多个鱼种全部落到 `fallbackFightStatics` 的小鲫鱼参数：

```text
f14 = 0.5
f15 = 4.0
```

结果是大鱼只有重量和 `f8` 拉力，没有外冲和摆头动作，表现为“像死鱼一样被直接拖回来”。
典型现象：`s.orfe`、`l_salmon`、`malma`、`golets_lev`、`neiva`、`kunja`
等 1～9kg 鱼在 4～20 秒内被收上来，几乎没有搏斗动作。

## Solution

保留官方逐鱼种表为最高优先级，把 `fishGroupStaticsFor` 从两个组扩展到全部
习性组：

```text
marine / apex / salmonid / ambush / cyprinid /
catfish / sturgeon / eel / small / default
```

每个组给出 `f9～f20`、`f5` 每克体力、`f6` 的组值：

- `salmonid`：持续发力 + 短促冲刺；
- `ambush`：受惊爆发；
- `catfish` / `sturgeon`：低速大扭矩；
- `eel`：持续扭动；
- `small`：动作碎但幅度小；
- `apex`：海鱼最高档外冲；
- `default`：未归组鱼种的中性偏强兜底。

组值是按常见体型/行为补的经验值，不是官服逐鱼种真值；拿到官方 14/14
样本后仍由 `officialSpeciesStatics` 逐种覆盖。

## Testing Decisions

- 逐组钉 `f14/f15`，确保不再出现 `f14=0.5/f15=4`；
- 钉 `riv_rb_trout`、`s.orfe`、`l_salmon`、`catfish`、`sturgeon`、`eel`、
  `gudgeon`、`cetorhinus_m`、`neiva`；
- 钉 `f5` 每克组值和 `f6` 组值；
- 回滚任一组条目，对应断言变红。

## Out of Scope

- 不声称这些组值是官方逐鱼种真值；
- 不恢复 2026-11-11 删除的分阶段张力/时长曲线；
- 不修改官方已落表的逐鱼种参数。
