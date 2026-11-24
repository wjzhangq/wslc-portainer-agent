# Portainer Agent for WSLC

上游 `portainer/agent` `develop`：`fb43126f1885996e3a8419c272215d53fdac90b0`。本分支 `feat/wslc-backend`。

## 构建

在 `agent/` 目录：

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o dist\agent-wslc.exe .\cmd\agent
```

`go.mod` 里的 `replace github.com/portainer/portainer => ../server-ce` 需要在 `agent` 的上一级有 Portainer CE 源码。本机检出的是 `release/2.45`，路径是 `D:\work\wslc\server-ce`。这份源码要求 Go 1.26.10，所以 `go.mod` 的 `go` 行从上游的 1.26.6 调到了 1.26.10。

## 安装

服务必须用拥有 wslc 数据的那个 Windows 用户，不要用 LocalSystem。本机是 `zhangwj50`。安装脚本会检查该用户的 `%LOCALAPPDATA%\wslc`。

以管理员打开 PowerShell：

```powershell
.\scripts\install.ps1 -Secret "<与 Portainer 里填写的相同>" -User "zhangwj50" -Password "<密码>"
```

脚本先停掉已有服务和同名计划任务，再复制 exe 到 `%ProgramData%\PortainerAgentWslc`。默认 `AGENT_HOST=127.0.0.1`，不放行入站 9001。授予“作为服务登录”，把 `WSLC=1`、`AGENT_SECRET`、`AGENT_PORT=9001`、`AGENT_HOST`、`LOG_LEVEL=INFO` 写进服务的 `Environment`，然后启动服务。目录 ACL 只保留 SYSTEM、Administrators 和该用户。

证书和 `agent.log` 写在服务工作目录。`AGENT_SECRET` 为空时进程拒绝启动，原因写在 `agent.log`。

监听之前还会检查 wslc：找不到 `wslc.exe`、同目录没有 `wslcsession.exe`、版本低于 3.0.3.0、`wslc info` 没有内核或会话管理器版本、或当前用户没有 `%LOCALAPPDATA%\wslc\settings.yaml`，进程都拒绝启动。失败原因和补充办法写在 `agent.log`，控制台运行时同时打在 stderr。安装脚本在停掉已有服务之前做版本和 `wslcsession.exe` 检查，不通过就不会改服务。常见补法是管理员执行 `wsl --update`（或 `winget install --id Microsoft.WSL -e`），然后用拥有容器的用户执行一次 `wslc info`。

### session 0

服务在 session 0 里调用 `wslc.exe`。监听前会跑一次 `wslc ps -a`，`agent.log` 里的 `wslc ps probe` 带有 `runtime=service` 和容器数量。`runtime=console` 是控制台或计划任务。数量为 0 或探针报错时，这次服务会话没有看见用户的容器。

验收方法：服务起来之后，带签名访问 `/containers/json`，应当能看到 `wild_flinders`。

如果看不到，改用登录时启动的计划任务（跑在该用户会话里，重启后要有人登录才会起来）。密钥写在仅该用户可读的 `agent.env`，不进启动脚本：

```powershell
.\scripts\install.ps1 -Secret "<secret>" -User "zhangwj50" -Mode LogonTask
```

开发时也可以不装服务，在控制台直接跑（先设好同样的环境变量）。两种跑法走同一套启动代码。换模式重装时，脚本会删掉另一种。

## 接入 Portainer

先在本机确认 `https://127.0.0.1:9001/ping` 返回 204。`/ping` 不需要签名。`/info` 和 `/containers/json` 没有合法签名时必须是 403。

- 远程：安装时加 `-ExposeLAN -RemoteAddress <Portainer 的 IP>`。Agent 绑 `0.0.0.0`，防火墙只接受该地址，环境地址填 `https://<这台机器的 IP>:9001`。
- 同机：Server 自己用 wslc 跑，保持默认 `127.0.0.1`，地址填 `https://host.wslc.internal:9001`。容器端口默认只绑 `127.0.0.1`，局域网访问 9443 需要 `-p 0.0.0.0:9443:9443`。

添加环境时类型选 Agent，Secret 与 `AGENT_SECRET` 相同，并跳过 TLS 校验（证书是启动时自签的）。签名内容只有共享密钥，不包含请求的方法、路径和 body。跳过校验时，截到过任意一个合法请求的人可以重放签名头。环境应显示为 Docker standalone。`/agents` 返回 503 是 standalone 的正常结果。`/browse/ls` 返回 404。

## 行为说明

Portainer 打开容器页时每隔几秒拉一次列表。每次列表是一次 `ps` 加一次批量 `inspect`，故意不做缓存。这个轮询会让会话 VM 几乎走不到 30 秒空闲关停，这是预期，不是泄漏。第一次 `ps` 或 `inspect` 可能赶上 VM 冷启动，所以短命令超时是 60 秒。

`logs -f` 和 `events` 会在服务器 `WriteTimeout`（30 分钟）处被掐断，客户端重连即可，与官方 Agent 相同。

私有仓库的 `X-Registry-Auth` 第一版不解析。需要私有镜像时，先在这台机器上用同一个用户执行 `wslc login`。

带签名的请求可以用 `wslc/testhelper` 发：

```powershell
go run ./wslc/testhelper -secret "<AGENT_SECRET>" -url https://127.0.0.1:9001/info
```

start、stop、restart、kill、rm、pull、rmi 不要对着 `wild_flinders` 试，用单独的测试容器。
