# Portainer Agent for WSLC

在 Windows 上跑的 [Portainer Agent](https://github.com/portainer/agent)（2.45.0）。`WSLC=1` 时，Docker API 请求不再走命名管道，而是由本进程调用 `wslc.exe` 完成。Portainer 把这台机器当作一台 Docker standalone 环境来管理。

上游基线是 `portainer/agent` `develop` 的 `fb43126`。实现规格见 [IMPLEMENTATION-v2.md](IMPLEMENTATION-v2.md)，安装和运行细节见 [agent/wslc/OPS.md](agent/wslc/OPS.md)。

## 第一版能做的事

- 容器：列表、详情、启动、停止、重启、强制停止、删除、日志
- 镜像：列表、详情、拉取、删除
- 资源占用（stats）和事件（events）
- HTTPS，沿用 Agent 启动时生成的自签证书
- 鉴权只用 `AGENT_SECRET`（官方 ECDSA 签名）。`/ping` 不签名，其余 Docker API 缺签名返回 403

容器创建、exec/attach、网络和卷管理、Swarm、Edge、Kubernetes 不在这一版里。对应路径返回 501。`/browse` 在 WSLC 模式下返回 404。`/agents` 返回 503，这是 standalone 引擎的正常结果。

## 依赖

- Windows，已安装并能使用的 `wslc.exe`（默认 `C:\Program Files\WSL\wslc.exe`）
- Go 1.26.10（`GOTOOLCHAIN=auto` 即可）
- 与 `agent/` 同级的 Portainer CE 源码。`go.mod` 用 `replace` 指向 `../server-ce`，这份检出不进本仓库

```powershell
git clone --depth 1 --branch release/2.45 https://github.com/portainer/portainer.git server-ce
```

## 构建

在 `agent/` 目录：

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o dist\agent-wslc.exe .\cmd\agent
```

`dist/` 已被忽略，不要提交二进制。

## 控制台运行

服务和工作目录都用拥有 wslc 数据的那个 Windows 用户。证书和 `agent.log` 写到 `%ProgramData%\PortainerAgentWslc`。`AGENT_SECRET` 为空时进程拒绝启动。

```powershell
$env:WSLC = "1"
$env:AGENT_SECRET = "<与 Portainer 里填写的相同>"
$env:AGENT_PORT = "9001"
$env:AGENT_HOST = "127.0.0.1"
$env:LOG_LEVEL = "INFO"
.\dist\agent-wslc.exe
```

`https://127.0.0.1:9001/ping` 返回 204，响应头 `Portainer-Agent: 2.45.0`。带签名的请求：

```powershell
go run ./wslc/testhelper -secret "<AGENT_SECRET>" -url https://127.0.0.1:9001/info
```

## 安装

用管理员 PowerShell，在 `agent/` 目录执行。服务账号必须是 wslc 数据所属的用户，不要用 LocalSystem。

```powershell
.\scripts\install.ps1 -Secret "<AGENT_SECRET>" -User $env:USERNAME -Password "<该用户的密码>"
```

脚本把 exe 复制到 `%ProgramData%\PortainerAgentWslc`，放行 TCP 9001，授予“作为服务登录”，写入 `WSLC=1` 等环境变量，然后启动服务 `PortainerAgentWslc`。

服务跑在 session 0。如果服务进程看不到当前用户的 wslc 会话，改用登录时启动的计划任务：

```powershell
.\scripts\install.ps1 -Secret "<AGENT_SECRET>" -User $env:USERNAME -Mode LogonTask
```

计划任务在该用户登录后才运行。

## 接到 Portainer

添加环境时类型选 Agent，Secret 与 `AGENT_SECRET` 相同，并跳过 TLS 校验。

- Server 在另一台机器：Agent 绑定 `0.0.0.0`，地址填 `https://<本机 IP>:9001`
- Server 和 Agent 在同一台机器、Server 自己跑在 wslc 里：地址填 `https://host.wslc.internal:9001`

环境应显示为 Docker standalone。私有镜像的 `X-Registry-Auth` 这一版不解析；需要时先用同一个 Windows 用户执行 `wslc login`。

## 许可

本仓库中的 Agent 代码来自 Portainer，遵循 [Apache License 2.0](LICENSE)。
