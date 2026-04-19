---
name: agmux
description: '说明 agmux 能做什么以及如何使用。适用于 agmux capabilities, quickstart, session lifecycle, exec/run, stream, sudo, forwarding, transfer, reconnect, troubleshooting.'
argument-hint: '请描述你的场景，例如：首次连接、执行命令、端口转发、文件传输、重连恢复、故障排查'
---

# agmux 能力与使用技能

## 技能产出

调用本技能时，应产出：

1. 针对当前场景的能力说明（简短、可决策）。
2. 最小可执行命令序列（可直接复制运行）。
3. 仅在必要时补充高级参数和变体。
4. 可验证的完成检查清单。

## 适用场景

当用户出现以下诉求时使用：

- 想知道 agmux 是什么、适不适合 agent 自动化。
- 需要从零开始的上手路径。
- 需要连接 SSH 或创建本地会话。
- 需要安全执行命令（sudo、超时、流式输出）。
- 需要管理会话生命周期（list/use/detach/attach/kill）。
- 需要端口转发、SFTP/SCP 传输、重连恢复、健康检查。

## 能力地图

将 agmux 总结为“非交互、脚本优先”的会话复用工具，核心能力包括：

- 命名 SSH 会话与本地会话。
- 结构化命令执行（stdout/stderr 分离，带退出码）。
- 长任务流式输出。
- 通过 stdin 的安全 sudo 执行。
- 本地与远程端口转发。
- SCP/SFTP 文件传输。
- detach/attach 生命周期管理与 daemon 持久化。
- 指数退避自动重连与转发恢复。

## 工作流

### 1. 澄清目标与约束

先确认：

- 目标是远程还是本地。
- 一次性操作还是重复操作。
- 是否需要长时间流式输出。
- 是否需要 sudo、转发或文件传输。
- 安全策略：优先密钥认证，密码为备选。

### 2. 确认 daemon 可用

先执行：

```bash
agmux ping
```

若未运行则启动：

```bash
agmux start
```

### 3. 创建或选择会话

SSH 会话（推荐密钥）：

```bash
agmux connect -u <user> -h <host> -n <session> -i <ssh_key_path>
```

密码认证备选：

```bash
agmux connect -u <user> -h <host> -n <session> -P <password>
```

本地会话：

```bash
agmux local -n <session>
```

可选：设置默认会话：

```bash
agmux use <session>
```

### 4. 执行命令

会话内执行：

```bash
agmux exec -n <session> "<command>"
```

带超时：

```bash
agmux exec -n <session> -t <seconds> "<command>"
```

长任务流式输出：

```bash
agmux exec -n <session> --stream "<command>"
```

安全 sudo 执行：

```bash
agmux exec -n <session> --sudo --sudo-password <password> "<command>"
```

一次性本地执行（不建会话）：

```bash
agmux run "<command>"
```

### 5. 可选高级操作

本地转发（local -> remote）：

```bash
agmux forward -n <session> -l <local_port> -r <remote_port>
```

远程转发：

```bash
agmux forward -n <session> -R -l <local_port> -r <remote_port>
```

查看与关闭转发：

```bash
agmux forwards
agmux forward-close <forward_id>
```

文件传输：

```bash
agmux scp -n <session> -put <local_path> <remote_path>
agmux scp -n <session> -get <remote_path> <local_path>
```

SFTP 操作：

```bash
agmux sftp -n <session> -c ls -p <remote_dir>
agmux sftp -n <session> -c mkdir -p <remote_dir>
agmux sftp -n <session> -c rm -p <remote_path>
```

### 6. 管理会话生命周期

```bash
agmux list
agmux detach -n <session>
agmux attach -n <session>
agmux kill <session>
```

需要时强制重连：

```bash
agmux reconnect -n <session>
```

### 7. 完成验证

每次都要确认：

- `agmux ping` 可成功返回。
- 目标会话在 `agmux list` 中状态符合预期。
- 命令输出与退出码符合预期。
- 转发或传输结果可在 `agmux forwards` 或目标路径中验证。

## 决策逻辑

- 一次性本地命令：优先 `agmux run`。
- 同一目标重复执行：建立会话后用 `agmux exec`。
- 需要实时可见输出：加 `--stream`。
- daemon 重启后 SSH 会话离线：使用 `agmux attach` 并补充密码或密钥。
- 需要远程暴露反向转发：使用 `-R`，仅在确有必要时使用 `--public`。

## 质量标准

- 示例命令尽量显式带 `-n <session>`。
- 认证优先密钥，密码作为备选。
- 明确提示 agmux 为非交互模式，不适合依赖 TTY 的全屏交互程序。
- 回答先给最小路径，再给可选高级方案。

## 输出模板

调用本技能时，按以下结构输出：

1. 一句话场景总结。
2. 最小命令序列。
3. 可选高级变体。
4. 验收检查清单。
5. 下一步安全动作（detach、保持会话或清理）。
