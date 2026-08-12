---
name: gssh
description: '说明 gssh 能做什么以及如何使用。适用于 gssh capabilities, quickstart, session lifecycle, exec/run, stream, json output, sudo, forwarding, transfer, troubleshooting.'
argument-hint: '请描述你的场景，例如：首次连接、执行命令、端口转发、文件传输、重连恢复、故障排查'
---

# gssh 能力与使用技能

## 技能产出

调用本技能时，应产出：

1. 针对当前场景的能力说明（简短、可决策）。
2. 最小可执行命令序列（可直接复制运行）。
3. 仅在必要时补充高级参数和变体。
4. 可验证的完成检查清单。

## 适用场景

当用户出现以下诉求时使用：

- 想知道 gssh 是什么、适不适合 agent 自动化。
- 需要从零开始的上手路径。
- 需要连接 SSH 或创建本地会话。
- 需要安全执行命令（sudo、超时、流式输出、JSON 结构化输出）。
- 需要管理会话生命周期（list/use/kill）。
- 需要端口转发、SFTP/SCP 传输、重连恢复、健康检查。

## 能力地图

将 gssh 总结为“非交互、脚本优先、CLI 与 ssh/scp 对齐”的会话复用工具，核心能力包括：

- ssh 风格用法：`gssh user@host` 连接、`gssh user@host "cmd"` 直接执行、`gssh scp src session:/dst` 传输。
- 默认密钥认证：ssh-agent + `~/.ssh` 默认密钥自动尝试，与 ssh 一致。
- 命名 SSH 会话与本地会话。
- daemon 免管理：任何命令需要时自动启动，崩溃后自动恢复（socket 残留自动清理）。
- 结构化命令执行：非流式默认输出 `{"stdout","stderr","exit_code"}` JSON（`--raw` 切回原始输出，`--stream` 实时推送）。
- 长任务流式输出（`--stream`）。
- 通过 stdin 的安全 sudo 执行。
- 本地与远程端口转发（`-L`/`-R` 与 ssh 语义一致）。
- SCP/SFTP 文件传输（scp 风格 `session:path` 语法）。
- 会话切换与重连（`use` 智能切换默认，自动恢复断开的 SSH 连接）。
- 指数退避自动重连与转发恢复；daemon 重启后密钥会话自动重连。

## 工作流

### 1. 澄清目标与约束

先确认：

- 目标是远程还是本地。
- 一次性操作还是重复操作。
- 是否需要长时间流式输出或 JSON 结构化结果。
- 是否需要 sudo、转发或文件传输。
- 安全策略：优先密钥认证（默认自动尝试 agent 和默认密钥），密码为备选。

### 2. daemon 无需手动管理

daemon 会在任何命令需要时自动启动，一般跳过此步。仅在需要显式控制时：

```bash
gssh ping     # 检查 daemon 状态（不会触发自动启动）
gssh start    # 显式启动（等待就绪后返回）
gssh server   # 前台运行（调试用）
```

故障排查时查看 daemon 日志：`~/.gssh/server.log`。

### 3. 连接或选择会话

SSH 连接（默认密钥认证，与 ssh 一致）：

```bash
gssh <user>@<host>                          # 自动尝试 ssh-agent + 默认密钥
gssh <user>@<host> -p <port>                # 指定端口
gssh <user>@<host> -i <ssh_key_path>        # 指定密钥
gssh <user>@<host> -n <session>             # 指定会话名（默认 user@host）
```

密码认证备选：

```bash
gssh <user>@<host> --pswd <password>
```

本地会话：

```bash
gssh local -n <session>
```

可选：设置默认会话或恢复断开的会话：

```bash
gssh use <session>                     # 切换默认（已连接的会话仅切换默认）
gssh use <session> --pswd <password>   # 恢复离线会话（密码）
gssh use <session> -i <ssh_key_path>   # 恢复离线会话（密钥）
```

### 4. 执行命令

ssh 风格直接执行（自动连接/复用会话，默认 JSON 输出）：

```bash
gssh <user>@<host> "<command>"
gssh <user>@<host> --raw "<command>"      # 原始输出（给人看/接管道）
gssh <user>@<host> --stream "<command>"   # 实时流式
```

命名会话内执行：

```bash
gssh exec -n <session> "<command>"                # 默认 JSON：{"stdout","stderr","exit_code"}
gssh exec -n <session> -t <seconds> "<command>"   # 带超时
gssh exec -n <session> --raw "<command>"          # 原始输出
gssh exec -n <session> --stream "<command>"       # 长任务流式输出
```

安全 sudo 执行：

```bash
gssh exec -n <session> --sudo --pswd <password> "<command>"
```

一次性本地执行（不建会话）：

```bash
gssh run "<command>"
```

### 5. 可选高级操作

本地转发（localPort:remotePort，与 ssh -L 语义一致）：

```bash
gssh forward -n <session> -L <local_port>:<remote_port>
```

远程转发（remotePort:localPort，与 ssh -R 语义一致）：

```bash
gssh forward -n <session> -R <remote_port>:<local_port>
```

查看与关闭转发（ID 可用列表中显示的 8 位前缀）：

```bash
gssh forwards
gssh forward-close <forward_id_or_prefix>
```

文件传输（scp 风格，一侧为 `session:path`）：

```bash
gssh scp <local_path> <session>:<remote_path>    # 上传
gssh scp <session>:<remote_path> <local_path>    # 下载
gssh sync <local_dir> <session>:<remote_dir>     # 增量同步
```

SFTP 操作（位置参数）：

```bash
gssh sftp -n <session> ls <remote_dir>
gssh sftp -n <session> mkdir <remote_dir>
gssh sftp -n <session> rm <remote_path>
```

### 6. 管理会话生命周期

```bash
gssh list                       # 或 gssh list --json
gssh use <session>              # 切换默认 / 恢复断开的会话
gssh use <session> --pswd <pw>  # 恢复离线会话（提供认证）
gssh kill <session>
```

### 7. 完成验证

每次都要确认：

- `gssh ping` 可成功返回。
- 目标会话在 `gssh list` 中状态符合预期。
- 命令输出与退出码符合预期（默认 JSON 输出可精确读取 exit_code）。
- 转发或传输结果可在 `gssh forwards` 或目标路径中验证。

## 决策逻辑

- 一次性本地命令：优先 `gssh run`。
- 一次性远程命令：`gssh <user>@<host> "cmd"`（自动复用会话连接）。
- 同一目标重复执行：命名会话 + `gssh exec -n <session>`。
- 需要程序解析结果：非流式默认即 JSON；给人看或接管道用 `--raw`。
- 需要实时可见输出或输出可能超过 4MB：加 `--stream`。
- daemon 重启后密钥会话自动重连；密码会话离线时使用 `gssh use <session> --pswd <password>` 恢复。
- 需要远程暴露反向转发：使用 `-R`，仅在确有必要时使用 `--public`。

## 质量标准

- 连接示例优先使用 `gssh <user>@<host>` 简写形式。
- 认证优先密钥（默认自动），密码作为备选（`--pswd`）。
- 明确提示 gssh 为非交互模式，不适合依赖 TTY 的全屏交互程序；密码短语密钥需先 ssh-add。
- 回答先给最小路径，再给可选高级方案。

## 输出模板

调用本技能时，按以下结构输出：

1. 一句话场景总结。
2. 最小命令序列。
3. 可选高级变体。
4. 验收检查清单。
5. 下一步安全动作（kill 会话或保持）。
