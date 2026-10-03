# server-authoritative-anticheat-go

进程内的服务端权威对局行为校验与作弊判定。客户端上报的角色操作与状态变更
通过本地回环（`Engine.Submit`，内存管道）送达服务端，服务端据此判定对局中
是否存在作弊。整条链路只在本进程内运行，不依赖任何外部服务，判定结论可在
本地复现与验证。

## 架构

```
Engine                 顶层入口：对局管理、本地回环提交入口
 ├─ RuleRegistry       规则集多版本注册表（版本不可变，开局绑定）
 ├─ VerdictStore       判定与复核记录（判定不可变，复核追加）
 └─ Session            一局对局的权威校验上下文（绑定一个规则版本）
     └─ playerLane     每玩家独立处理通道（互不影响）
         ├─ sequencer  排序 / 去重 / 迟到识别（序号机）
         ├─ validate   防篡改校验值、伪造时间识别
         └─ judge      规则评估（速度/位移/攻击频率/伤害/资源）
```

## 判定语义

### 操作应用语义（精确一次、按序）

- 每个玩家的操作携带单调递增序号 `Seq` 与客户端时间 `ClientTime`。
- 乱序到达的操作进入有界重排缓冲，序号连续后按序应用；最终应用结果与
  "按序号顺序单次处理"完全一致。
- 重复提交（同序号同内容）被拒绝且不会被应用两次。
- 水位线以下的冲突操作（同序号不同内容）按"序号回退"拒绝，
  不得改写已发布的权威状态。

### 规则与容差口径

每条规则把观测值与允许上限比较，定义超限比例：

```
r = observed / limit - 1        （上限类规则：速度、位移、伤害、资源）
r = limit / observed - 1        （下限类规则：攻击间隔）
```

- `r <= Tolerance`：视为正常网络抖动，不计违规；
- `r > Tolerance`：累计一次"连续违规"，**连续**达到 `ViolationWindow` 次
  才产出正式判定——单次延迟尖峰不会误判；
- 严重程度按当前连续违规段内的最大超限比例分档：

| 区间 | 等级 |
| --- | --- |
| `r <= Tolerance` | 不违规 |
| `r <= BorderlineBand` | borderline（边界情形，清楚归类并给出依据） |
| `r <= MajorBand` | minor |
| `r <= CriticalBand` | major |
| 其余 | critical |

### 判定结论（Verdict）

每条判定可追溯到具体操作与所触发的规则，包含：

- `RuleID` / `RuleVersion`：触发的规则与规则版本；
- `OpSeq`：触发判定的操作序号；
- `Observed` / `AllowedMin` / `AllowedMax`：观测值与允许范围（含容差）；
- `Severity`：可解释的严重程度；
- `LogicalTime`：逻辑时钟（已应用操作计数），不依赖墙钟。

判定只依赖操作字段与逻辑时钟，因此**同一对局在同一规则版本下重放
（`Replay`）必然得到与在线判定逐条一致的结论**（判定 ID 由存储分配，
不参与比较）。

### 异常输入识别

所有异常输入都被显式拒绝并给出互相可区分的理由，任何输入都有回执
（`Receipt`），不会崩溃或静默丢弃：

| RejectReason | 含义 |
| --- | --- |
| `seq_rollback` | 序号回退（水位线以下的冲突内容） |
| `seq_gap` | 序号跳跃超过 `MaxSeqGap` |
| `duplicate` | 重复提交（已应用或已缓冲） |
| `forged_time` | 客户端时间回退超过 `MaxClockSkewMs` |
| `tampered` | 防篡改校验值不符（字段被修改） |
| `queue_full` | 在途操作达到上界（背压） |
| `reorder_buffer` | 乱序缓冲达到上界 |
| `session_closed` / `unknown_match` / `unknown_player` | 对局/玩家状态非法 |

防篡改口径：客户端用对局级派生密钥对操作全部权威字段计算 FNV-1a
校验值（`Sign`），服务端重算比对，任何字段被篡改都会失败。

### 并发与一致性

- 每玩家一条独立处理通道：慢玩家或恶意高频上报只会填满自己的在途
  名额并被背压拒绝，不阻塞其他玩家。
- 在途操作（`InboxCapacity`）、乱序缓冲（`MaxReorderBuffer`）、判定记录
  （`MaxVerdictsPerSession`）、对局数（`MaxSessions`）都有可配置上界。
- 权威状态以不可变快照（`Snapshot`）发布，版本号单调递增；任何时刻
  读到的都是自洽的单个已提交版本（`Σ AppliedSeq == Version`）。

### 规则版本化

- `RuleRegistry` 注册不可变的规则版本，新注册版本成为当前生效版本。
- 对局在 `StartMatch` 时绑定当时生效的版本，之后规则升级不影响
  进行中的对局，已结束对局的结论也不会被改写。
- 历史判定可按规则版本查询：`VerdictStore.VerdictsByRuleVersion`。

### 申诉复核

- 复核（`Review`）以追加记录的方式存在，**不改写原始判定**。
- 复核结论：`upheld`（维持）/ `overturned`（推翻）/ `adjusted`（调整等级）。
- `EffectiveSeverity` 返回考虑全部复核后的有效等级；原始记录可通过
  `VerdictStore.Verdict(id)` 原样追溯。

## 配置项

### Config（容量上界）

| 字段 | 默认 | 含义 |
| --- | --- | --- |
| `InboxCapacity` | 256 | 每玩家在途操作上界，满即背压拒绝 |
| `MaxReorderBuffer` | 64 | 每玩家乱序缓冲容量 |
| `MaxVerdictsPerSession` | 4096 | 每对局判定记录上界（不影响状态推进） |
| `MaxSessions` | 64 | 同时进行的对局数上界 |

### RuleSet（规则与容差）

| 字段 | 默认(v1) | 含义 |
| --- | --- | --- |
| `MaxSpeed` | 10 | 最大移动速度（单位/秒） |
| `MaxStepDistance` | 5 | 单次操作最大位移 |
| `MinAttackIntervalMs` | 200 | 最小攻击间隔（毫秒） |
| `MaxDamagePerHit` | 100 | 单次伤害上限 |
| `MaxResourcePerSecond` | 50 | 资源获取速率上限 |
| `Tolerance` | 0.15 | 容差比例：内容忍正常网络抖动 |
| `BorderlineBand` | 0.35 | 边界带上限（超限比例） |
| `MajorBand` / `CriticalBand` | 1.0 / 3.0 | 严重等级分档 |
| `ViolationWindow` | 3 | 连续违规窗口：达到才产出判定 |
| `MaxSeqGap` | 64 | 允许的最大序号跳跃 |
| `MaxClockSkewMs` | 500 | 客户端时间允许的最大回退 |

## 使用示例

```go
e := anticheat.NewEngine(anticheat.DefaultConfig(), nil, nil)
s, _ := e.StartMatch("match-1", []string{"alice"})

op := anticheat.Sign(anticheat.Operation{
    MatchID: "match-1", PlayerID: "alice", Seq: 1,
    ClientTime: 1000, Type: anticheat.OpMove, Position: anticheat.Vec3{X: 3},
}, "match-1", "alice")
receipt := s.Submit(op)          // 显式回执

snap := s.Snapshot()             // 自洽的已提交版本
verdicts := e.Store().Verdicts("match-1")

// 重放验证：结论与在线判定逐条一致
replayed := anticheat.Replay("match-1", s.Journal(),
    e.Registry().Get /* 或保存的规则版本 */, anticheat.DefaultConfig())
_ = replayed
_ = receipt
_ = snap
_ = verdicts
```

## 测试

```sh
go test ./...        # 单元与并发测试
go test -race ./...  # 竞态检测
```

覆盖场景：乱序、重复、迟到、伪造时间、篡改字段、序号跳跃/回退、
并发校验与背压、规则版本切换、重放一致性、正常网络抖动不误判、
容差边界分级、申诉复核、各类容量上界。
