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

推送 `v*` tag 时，GitHub Actions 会编译 `agent-wslc.exe` 并发布到对应的 Release。压缩包里是 `dist\agent-wslc.exe` 和 `scripts\install.ps1`，解压后在该目录用管理员 PowerShell 运行 `.\scripts\install.ps1`。带连字符的 tag（例如 `v1.0.0-rc1`）会标成预发布。

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

脚本先停掉已有的服务和同名计划任务，再把 exe 复制到 `%ProgramData%\PortainerAgentWslc`。默认 `AGENT_HOST=127.0.0.1`，不添加入站防火墙规则。然后授予“作为服务登录”，写入 `WSLC=1` 等环境变量，启动服务 `PortainerAgentWslc`。目录 ACL 只留给 SYSTEM、Administrators 和该用户。

服务跑在 session 0。启动时会做一次 `wslc ps -a`，结果写在 `agent.log` 的 `wslc ps probe`。如果这次看不到容器，改用登录时启动的计划任务：

```powershell
.\scripts\install.ps1 -Secret "<AGENT_SECRET>" -User $env:USERNAME -Mode LogonTask
```

计划任务绑到该用户，只在他登录后运行。密钥放在同目录的 `agent.env`，不写进启动脚本。两种模式不会同时留着。

要让另一台机器上的 Portainer 连接，显式放开监听和防火墙，并把来源限制到 Server 的地址：

```powershell
.\scripts\install.ps1 -Secret "<AGENT_SECRET>" -User $env:USERNAME -Password "<密码>" -ExposeLAN -RemoteAddress "<Portainer 的 IP>"
```

## 接到 Portainer

添加环境时类型选 Agent，Secret 与 `AGENT_SECRET` 相同，并跳过 TLS 校验。

签名只覆盖 `AGENT_SECRET`，不覆盖方法、路径和 body。跳过 TLS 校验时，见过任意一个合法请求的人可以重放那两个签名头。默认只监听本机；`-ExposeLAN` 才会把 9001 暴露到网络。

- Server 在另一台机器：安装时加 `-ExposeLAN -RemoteAddress <Portainer 的 IP>`，地址填 `https://<本机 IP>:9001`
- Server 和 Agent 在同一台机器、Server 自己跑在 wslc 里：保持默认的 `127.0.0.1`，地址填 `https://host.wslc.internal:9001`

环境应显示为 Docker standalone。私有镜像的 `X-Registry-Auth` 这一版不解析；需要时先用同一个 Windows 用户执行 `wslc login`。

## 许可

本仓库中的 Agent 代码来自 Portainer，遵循 [Apache License 2.0](LICENSE)。
