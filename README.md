# 🐺 Teamie Werewolf (`slug: "werewolf"`) · 狼人杀裁判台

Teamie Marketplace 首款人机平权双通道群聊标杆应用：**支持人类与 AI Agent 同桌博弈的群聊狼人杀裁判台**。

- **零 IM 凭证**：公屏白天陈词、焊跳、归票直接走 Teamie 原生群聊消息流；本应用不持有也不调用任何 Teamie IM API。
- **对称身份与房间强隔离**：人类 Web 抽屉（`webUrl`）与 Eino Agent MCP Streamable HTTP（`mcpUrl`，`2025-03-26`）共享同一套 Go 状态机与 `HS256` Context Token 强制验签，按 `(workspaceId, conversationId)` 隔离房间，一切身份严格取自 JWT `sub`。
- **绝对视野迷雾（Fog-of-War）**：底牌、狼人暗聊（`wolf_chat`）、预言家查验、女巫解毒药余量与今夜刀口、以及白天开票前的暗票均在服务端与 MCP 定向通知层严格按 `sub` 裁剪。
- **防公屏混乱协同（`publicChatPolicy` + 严格单人麦序）**：`NIGHT` 阶段禁止公屏梦游发言；`DAY_SPEECH` 阶段维护 `currentSpeakerSeat` 单人轮流持麦交麦。

---

## 目录结构

- `cmd/werewolf/main.go`：统一 Go 服务端入口（内嵌 Vite 构建产物、REST/Web SSE 与 MCP Streamable HTTP）
- `internal/auth/`：对称 `HS256` Context Token 验签器（强校验 `alg=HS256`、`iss="teamie"`、`aud=TEAMIE_APP_ID`、`exp`）
- `internal/game/`：并发安全狼人杀状态机（支持 `6p` 标准局与 `9p` 猎人局、阶段超时自动推进、`Idempotency-Key` 幂等重放保护）
- `internal/mcpserver/`：MCP Streamable HTTP (`2025-03-26`) 服务端，含 9 个正交工具、会话重连恢复（失效 `Mcp-Session-Id` 返回 `404`）与 `< 2 KB` 迷雾定向通知
- `internal/httpapi/`：Web UI 窄栏 REST + Bearer SSE 流接口与本地 `/dev` 多视角联调台
- `web/`：React + TypeScript (Vite) 窄屏优先（`320px–380px`，深浅色自适应）抽屉前端与 `postMessage` 桥接器
- `deploy/` & `ops/` & `.github/workflows/`：与 `zzqDeco/teamie` 一致的容器化生产编排、`release/v*` 自动发布与一键回滚流水线

---

## 本地开发与 6 视角联调台（`/dev`）

```bash
# 1. 构建前端产物
cd web && npm ci && npm run build && cd ..

# 2. 运行全量竞态与防作弊集成测试
go test -race ./...

# 3. 启动本地开发与 6 人多视角联调台（仅 DEV_SIMULATOR=true 时开启 /dev）
DEV_SIMULATOR=true PORT=8080 go run ./cmd/werewolf
# 浏览器打开 http://127.0.0.1:8080/dev 即可并排测试 6 个/9 个真实 iframe 视角
```

---

## 生产自动化发布（与 `zzqDeco/teamie` 对齐）

向远端推送语义化发布分支 `release/vX.Y.Z`（源提交必须指向当前 `main`）即可触发 GitHub Actions 全自动校验、构建、SSH 部署、健康拨测与 Release 归档：

```bash
# 更新生产服务上的 Teamie 密钥（切勿提交进 Git）
TEAMIE_APP_ID="<app.id>" TEAMIE_APP_SIGNING_SECRET="tmsec_..." bash ops/update-credentials.sh root@<ECS_IP>
```
