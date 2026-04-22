# TrapTunnel Node 改造方案

## 1. 目标

本文档描述从当前 `sender + receiver` 实现演进到统一 `node` 架构的改造步骤、阶段目标、风险控制和上线建议。

改造目标：

1. 保留当前链路可用性
2. 逐步收敛为单一 `node` 模型
3. 支持 relay、fanout、inject 修正和 export
4. 降低对现网 `A-F`、`G/H` 的切换风险

## 2. 当前基线

当前代码基线：

- `sender` 负责本地抓包并向单个主用上游转发
- `receiver` 负责接收后注入本地网络
- 隧道帧已经具备 `node_id + seq + raw_ip_packet` 基础结构

当前环境基线：

- `A/B`、`C/D`、`E/F` 三组入口节点
- `G/H` 为中心 `inManage` 集群节点
- HMS 仍在 `A-F` 上直接监听 `162/UDP`

## 3. 改造原则

### 3.1 原则一：先统一内核，再统一程序名

先完成内部能力模型和流水线统一，再将外部运行形式收敛为 `node`。避免在功能改动和部署改动同时发生时放大风险。

### 3.2 原则二：原始帧优先

- 中继和导出优先保留原始 frame
- 注入修正只对本地副本生效

### 3.3 原则三：边缘入口 HA 与业务消费 HA 分离

- 三组 VIP 继续承担设备入口职责
- HMS 或其替代程序的主备切换建立在 export 消费层

### 3.4 原则四：逐步替换 HMS 接入方式

- 首阶段保留现有 UDP/162 接入方式
- 第二阶段新增 export，供新告警程序直接消费

## 4. 目标阶段划分

## 阶段 1：抽取统一 frame 与流水线

目标：

- 将当前 `sender/receiver` 中重复的配置、协议和日志逻辑收敛为共享模块
- 明确 `frame`、`sink`、`profile` 等内部抽象

主要工作：

- 抽取 frame 编解码模块
- 抽取共享配置和日志模块
- 抽取内部分发总线

交付结果：

- 保持现有 `sender` / `receiver` 外部行为不变
- 为后续 `node` 能力组合打基础

## 阶段 2：实现统一 node 二进制

目标：

- 新增 `node` 二进制
- 支持通过 profile 或显式开关启用能力

主要工作：

- 增加统一入口 `node/main.go`
- 支持 `capture`、`ingress`、`egress`、`inject`
- 保留 `sender` / `receiver` 作为兼容入口或薄封装

交付结果：

- `edge`、`relay`、`sink` 三类节点可运行

## 阶段 3：实现 relay 和多上游 fanout

目标：

- 支持一个 node 同时做本地采集和下游 ingress
- 支持 `[[egress.groups]]` 的“组内主备、组间扇出”配置模型

主要工作：

- 将内部发送对象从 `PacketData` 升级为 `frame`
- ingress 接收到的 frame 可直接进入 egress
- 每个 fanout group 独立连接、独立缓冲、独立重连
- 热重载支持修改 egress group

交付结果：

- A 类节点可作为“本地采集 + 下游汇聚 + 上游转发”的 relay

## 阶段 4：实现 inject 修正

目标：

- 在 `inject` 路径增加 SNMPv1 `agent-addr` 修正

主要工作：

- 解析 UDP payload 中的 SNMPv1 Trap-PDU
- 提取 `agent-addr`
- 对注入副本修正源 IP
- 重新计算 IPv4/UDP 校验和

交付结果：

- `G/H` 上的 node 可兼容 `inManage` 的解析限制

## 阶段 5：实现 export

目标：

- 提供对外 TCP 订阅接口

主要工作：

- 增加 export listener
- 广播原始 frame 给订阅客户端
- 每客户端独立缓冲
- 慢客户端保护主链路

交付结果：

- 新告警程序可直接通过 TCP 获取原始 Trap

## 阶段 6：部署切换

目标：

- 平滑替换现网组件

主要工作：

- `A-F` 由 `sender` 切换为 `node edge/relay`
- `G/H` 由 `receiver` 切换为 `node sink`
- 验证 `inManage` 收到修正后的 SNMPv1 Trap
- 新程序逐步从 `162/UDP` 切换到 `export`

交付结果：

- 完成从双程序模型到统一 node 模型的线上迁移

## 5. 代码层面改造清单

### 5.1 目录结构建议

建议从当前双入口结构演进为：

```text
cmd/
  node/
  sender/
  receiver/
internal/
  config/
  logging/
  frame/
  capture/
  ingress/
  egress/
  inject/
  export/
  pipeline/
```

### 5.2 关键重构点

1. `sender` 当前内部发送对象是 `PacketData`
   - 需要升级为统一 `frame`
2. `receiver` 当前入口和注入耦合较紧
   - 需要拆分为 `ingress` 和 `inject`
3. 当前多目标发送是 failover 语义
   - 需要扩展为 `fanout group + failover member`
4. 当前日志与配置散落在两个入口程序中
   - 需要提取共享模块

## 6. 配置改造方案

### 6.1 现有配置

- `sender.conf`
- `receiver.conf`

### 6.2 目标配置

统一为 `node.toml`：

```toml
[node]
id = 101
profile = "relay"

[capture]
enabled = true
listen_ports = [162]

[ingress]
enabled = true
listen = "0.0.0.0:10000"

[egress]
enabled = true

[[egress.groups]]
members = ["172.16.1.10:10000", "172.16.1.11:10000"]

[[egress.groups]]
members = ["172.16.2.10:10000"]

[inject]
enabled = false
ip = "127.0.0.1"
port = 1162
snmpv1_agent_addr_override = false

[export]
enabled = true
listen = "0.0.0.0:12000"
```

### 6.3 配置兼容策略

建议提供以下过渡能力：

- `node` 能兼容读取旧版 `sender.conf`
- `node` 能兼容读取旧版 `receiver.conf`
- 或提供配置转换脚本，将旧配置转换为 `node.toml`

## 7. 部署方案

### 7.1 边缘侧 A-F

建议角色：

- `A-F` 初期使用 `profile = edge`
- 若未来需要 `B/C -> A -> G/H` 这类汇聚场景，则 A 使用 `profile = relay`

说明：

- 三组 VIP 继续保持不变
- 设备入口方式不需要改

### 7.2 中心侧 G-H

建议角色：

- `G/H` 使用 `profile = sink`

说明：

- 启用 `ingress + inject`
- 启用 `snmpv1_agent_addr_override`
- `inject.port` 设为 `162`

### 7.3 HMS 与新告警程序

过渡期：

- HMS 继续从 `162/UDP` 获取 Trap

目标期：

- 新告警程序改为订阅 `export`
- 将业务处理高可用建立在 export 消费端，而不是设备入口 VIP 端

## 8. 验证方案

## 8.1 功能验证

需要覆盖：

1. 本地采集
2. ingress 接收
3. relay 转发
4. fanout + failover
5. inject 注入
6. export 订阅
7. SIGHUP 热重载

## 8.2 协议验证

需要确认：

- frame 边界正确
- `node_id` 不串号
- relay 后 `seq` 不重置
- 多 group 扇出互不影响

## 8.3 业务验证

需要重点确认：

- `inManage` 是否按修正后的源 IP 正确识别设备
- 旧 HMS 是否继续可用
- 新程序是否可稳定消费 export 流

## 9. 风险与控制

### 9.1 配置复杂度上升

控制：

- 引入 `profile`
- 增加配置校验
- 为典型场景提供模板

### 9.2 本机回环风险

控制：

- 启动时校验 `inject.port` 与 `capture.listen_ports` 不冲突

### 9.3 拓扑环路风险

控制：

- 首版通过部署规范避免
- 文档中明确“只允许向上游配置 egress”

### 9.4 慢消费者拖垮主链路

控制：

- export 客户端独立缓冲
- 超时断开慢客户端

### 9.5 迁移期双体系并存

控制：

- 保持 sender/receiver 兼容入口一段时间
- 分阶段替换节点，不做一次性全网切换

## 10. 回滚方案

每个阶段都应具备独立回滚能力：

- 边缘节点可随时退回现有 `sender`
- 中心节点可随时退回现有 `receiver`
- 新告警程序异常时，不影响 `inManage` 的 `inject` 路径

关键要求：

- 在 `node` 未验证稳定前，不应下线现有部署模板
- 保留旧 systemd service 和配置文件转换能力

## 11. 推荐上线顺序

建议按以下顺序推进：

1. 完成 `node` 代码抽象与单机测试
2. 先在实验环境验证 `inject + agent-addr` 修正
3. 在 `G/H` 试点切换为 `node sink`
4. 保持 `A-F` 仍使用现有 sender，验证中心兼容逻辑
5. 再将 `A-F` 切到 `node edge`
6. 最后引入 `export`，让新程序接入

这样可以先解决 `inManage` 解析问题，再推进更大的消费层改造。

## 12. 改造 Checklist

以下 checklist 用于跟踪 `node` 改造的实际落地进度。建议在实施过程中按阶段逐项勾选。

### 12.1 基础抽象

- [ ] 确定 `cmd/` 和 `internal/` 的新目录结构
- [ ] 抽取统一 `frame` 数据结构
- [ ] 抽取 `frame` 编解码模块
- [ ] 抽取共享日志初始化模块
- [ ] 抽取共享配置加载模块
- [ ] 引入统一 `profile` 概念：`edge / relay / sink / full`
- [ ] 建立统一 pipeline / sink 抽象

### 12.2 Node 最小闭环

- [ ] 新建统一入口 `cmd/node`
- [ ] 支持 `capture` 能力
- [ ] 支持 `ingress` 能力
- [ ] 支持基础 `egress` 能力
- [ ] 支持 `inject` 能力
- [ ] 支持 `profile = edge`
- [ ] 支持 `profile = sink`
- [ ] 跑通 `edge -> sink -> inject` 的最小链路

### 12.3 Relay 与多上游转发

- [ ] 将内部发送对象从 `PacketData` 升级为统一 `frame`
- [ ] ingress 接收到的 `frame` 可直接进入 egress
- [ ] 支持 `profile = relay`
- [ ] 支持 `[[egress.groups]]` 配置结构
- [ ] 实现组内 failover
- [ ] 实现组间 fanout
- [ ] 每个 group 拥有独立连接和缓冲
- [ ] 热重载支持修改 egress group

### 12.4 Inject 修正

- [ ] 在 `inject` 路径解析 UDP payload
- [ ] 识别 SNMPv1 Trap-PDU
- [ ] 提取 `agent-addr`
- [ ] 仅在 inject 副本中覆盖源 IP
- [ ] 重新计算 IPv4 校验和
- [ ] 重新计算 UDP 校验和
- [ ] 确认主链路 `frame` 不被污染
- [ ] 验证 `G/H -> inManage` 识别结果正确

### 12.5 Export

- [ ] 增加 export listener
- [ ] 定义 export 首版输出协议
- [ ] 支持多个 export 客户端订阅
- [ ] 每个客户端独立缓冲
- [ ] 慢客户端不会拖垮主链路
- [ ] 验证新程序可稳定消费 export 流

### 12.6 配置与兼容

- [ ] 统一配置格式切换为 `node.toml`
- [ ] 配置结构支持 `capture / ingress / egress / inject / export`
- [ ] 增加配置合法性校验
- [ ] 增加 `inject.port` 与 `capture.listen_ports` 冲突校验
- [ ] 兼容读取旧版 `sender` 配置
- [ ] 兼容读取旧版 `receiver` 配置
- [ ] 或提供旧配置到 `node.toml` 的转换工具

### 12.7 部署与运行

- [ ] 保留旧 `sender` / `receiver` 作为兼容入口
- [ ] 新增 `node` 的 systemd/service 模板
- [ ] 更新构建脚本，支持 `node`
- [ ] 更新安装/卸载/验证脚本
- [ ] 补充 `edge / relay / sink` 示例配置
- [ ] 补充 `A-F` 和 `G/H` 的部署示例

### 12.8 测试与验证

- [ ] 验证本地采集
- [ ] 验证 ingress 接收
- [ ] 验证 relay 转发
- [ ] 验证 fanout + failover
- [ ] 验证 inject 注入
- [ ] 验证 export 订阅
- [ ] 验证 SIGHUP 热重载
- [ ] 验证 `node_id + seq` 在 relay 后保持不变
- [ ] 验证 `inManage` 对 SNMPv1 Trap 的设备识别正确
- [ ] 验证旧 HMS 在过渡期仍可工作

### 12.9 上线切换

- [ ] 在实验环境完成单机测试
- [ ] 在实验环境完成端到端链路测试
- [ ] 先在 `G/H` 试点切换为 `node sink`
- [ ] 保持 `A-F` 仍使用旧 sender 验证中心链路
- [ ] 再将 `A-F` 切换为 `node edge`
- [ ] 验证 `A-F -> MQ` 主链路稳定
- [ ] 验证 `A-F -> G/H -> inject -> inManage` 修复链路稳定
- [ ] 最后引入 export 给新程序接入
- [ ] 确认回滚路径和旧部署模板可用

## 13. 结论

本改造方案的核心不是简单把 `sender` 改名为 `node`，而是把当前系统重构为：

- 统一的 frame
- 统一的能力模型
- 统一的配置和部署抽象

按该路线推进，可以在不破坏现网入口拓扑的前提下，逐步实现 relay、fanout、兼容注入和 export 四项关键能力。
