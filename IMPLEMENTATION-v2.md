# Portainer Agent for WSLC（实现说明）

本文是实现规格，不是探测笔记。范围、接入方式和改动点按 2026-11-24 的确认结果写定。命令、路径和 JSON 字段来自本机实测；Agent 侧的结论来自 `portainer/agent` `develop` 分支源码核对。

## 已确认的决策

| 项 | 决定 |
| --- | --- |
| 语言 | Go。本机是 `go1.25.1 windows/amd64`。编译时以 fork 的 `go.mod` 为准，`CGO_ENABLED=0` |
| 路线 | Fork [portainer/agent](https://github.com/portainer/agent) 的 `develop`（2.45.0），分支 `feat/wslc-backend`。不新写一套 Agent |
| 进程形态 | Windows 原生进程，调用 `wslc.exe`。不放进容器，不用 cgo |
| 第一版功能 | 容器列表、详情、启动、停止、重启、强制停止、删除、日志；镜像列表、拉取、删除；stats；events |
| 接入 | 第一版就启用 HTTPS（沿用 Agent 自签证书）；验证只用官方签名机制（`AGENT_SECRET` 即共享密钥），不加 token 通道 |
| 这一步 | 只更新本文。克隆仓库和写代码另开 |

明确不做：容器创建/运行、exec/attach、网络和卷的管理、Swarm、Edge、Kubernetes。

## 本机环境

2026-11-24 在这台 Windows 上核对过：

| 项 | 值 |
| --- | --- |
| wslc | 3.0.3.0，`C:\Program Files\WSL\wslc.exe` |
| 会话进程 | `C:\Program Files\WSL\wslcsession.exe` |
| 设置 | `%LOCALAPPDATA%\wslc\settings.yaml`。当前 `storagePath` 是 `d:\wslc-data` |
| SDK | `C:\Windows\System32` 和 `C:\Program Files\WSL` 下都没有 `wslcsdk.dll` 或头文件 |
| Go | `C:\Program Files\Go\bin\go.exe`，1.25.1 |

`wslc info` 是中文纯文本，没有 `--format json`。帮助和错误信息也是中文。判断失败只能看退出码和 stderr，不能匹配英文字符串。

本机已有一个已退出容器 `wild_flinders`（`ac3d3bff1501…`）和两个镜像。下面的字段都来自它们，没有为了写文档去创建或删除对象。

## 架构

官方 Agent 不是一组 `/containers/json` 处理器。请求经过签名校验后，落到 `http/proxy.LocalProxy`，再由它去连本机 Docker：

```
Portainer Server
  HTTPS :9001
  X-PortainerAgent-PublicKey
  X-PortainerAgent-Signature
        │
        ▼
http.APIServer                         自签证书，ListenAndServeTLS
  http/handler.ServeHTTP               写 Portainer-Agent / -API-Version / -Platform 响应头
    http/security                      校验签名。AGENT_SECRET 非空时走 secret 模式
      http/handler/docker              clusterService == nil 时，所有 Docker API 路径进 LocalProxy
        http/proxy/local.go            今天：RoundTrip 到命名管道 //./pipe/docker_engine
                                       改成：交给 wslc 后端
              │
              ▼
        wslc.exe（子进程）
```

Windows 上管道在 `http/proxy/local_namedpipe.go` 的 `NewLocalProxy` 里写死为 `//./pipe/docker_engine`。wslc 没有这根管道。

改动收在三处（比上一版多一处，原因见下）：

1. `cmd/agent/main.go`：加 `WSLC=1` 启动分支。不走 Docker 探测，并设置 `ContainerPlatform`。
2. `http/proxy/local.go`：`WSLC=1` 时 `LocalProxy` 持有 wslc handler，不再拨管道。
3. `http/handler/handler.go`：WSLC 模式下不接 `/browse` 路由。理由见“必须禁用 /browse”。

`clusterService` 保持 `nil`。`docker_operation.go` 里 `clusterService == nil` 时会把全部 Docker API 请求交给 `LocalProxy`，Swarm 聚合、serf、Edge、Kubernetes 都不会进来。这些目录不要改。

### Agent 侧已核实的行为

| 事项 | 核实结果 |
| --- | --- |
| `Portainer-Agent-Platform` 响应头 | 每个响应都写。**值不是 PlatformDocker 时 Portainer 把环境当 Kubernetes 处理**。裸 Windows 宿主上 `os.DetermineContainerPlatform()` 不会返回 Docker，所以 WSLC 分支必须显式设 `ContainerPlatform = agent.PlatformDocker` |
| TLS 证书路径 | `agent.TLSCertPath` / `TLSKeyPath` 是**相对路径** `cert.pem` / `key.pem`，解析于进程工作目录。服务的工作目录是 `C:\Windows\System32`，必须先固定工作目录再生成证书 |
| 72 小时自动关停 | `securityShutdown` 检查 `signatureService.IsAssociated()`；`AGENT_SECRET` 非空时它恒为 true，不会误关。已读 `crypto/ecdsa.go` 确认 |
| `/agents` | `clusterService == nil` 时返回 503（“standalone 引擎上不可用”）。这是官方 standalone+Agent 部署的预期行为，Portainer 容忍它。不要修，也不要当 bug 报 |
| `/browse`（V2） | 不带 `volumeID` 时接受任意 `path`，ls/get/put/delete/rename 直接作用于 Agent 自己的文件系统。在容器里是容器 FS；在 Windows 宿主上是**真实磁盘**。必须在 WSLC 模式下禁用，返回 404。V1（`/v1/browse/...`）靠 Docker 卷解析，会自然失败，但一并禁掉 |
| `/websocket`、`/kubernetes` | 依赖 Docker 或 K8s，exec 不在第一版，保持原样（会失败，与“未实现”一致） |
| `/v1.xx` 前缀 | Portainer 的 docker client 带版本前缀发请求（`/v1.44/containers/json`），顶层 `ServeHTTP` 的 switch 会让它落到 docker 代理分支，但**不能依赖上游替我们剥前缀**——wslc handler 自己要剥，幂等地做两次无害 |
| HTTP 超时 | 官方 `ReadTimeout: 120s`、`WriteTimeout: 30m`。后果：`logs -f` 和 `events` 长连接在 30 分钟处被服务器掐断。这是官方 Agent 的既有行为，保留不动 |

## 启动时的 WSLC 分支

`cmd/agent/main.go` 在 Docker/Podman 分支里会做三件 wslc 上不存在的事：`GetRuntimeConfigurationFromDockerEngine()`、`GetContainerIpFromDockerEngine()`、Swarm 组集群。任何一件失败都会 `log.Fatal` 退出，所以 `WSLC=1` 必须整体跳过该分支，改成：

1. **先固定工作目录**：`os.Chdir` 到 `%ProgramData%\PortainerAgentWslc`（不存在则创建）。证书、日志都落在这里。
2. `ContainerPlatform = agent.PlatformDocker`。这条不过，Portainer 会把环境当 Kubernetes。
3. `RuntimeConfig.DockerConfig.EngineType = standalone`；`NodeName` 用 Windows 主机名。
4. `advertiseAddr` 用 `AGENT_HOST`；它是 `0.0.0.0` 时改用本机一个非回环 IPv4 传给 `GenerateCertsForHost`（Portainer 侧反正跳过证书校验，证书是自签的）。
5. 不创建 `clusterService`。
6. `AGENT_SECRET` 为空则拒绝启动。官方允许“第一个连上来的 Portainer 自动绑定”，那会把未设密钥的 9001 暴露出去，不走那条路径。

### 服务端验证机制（不加 token 通道）

已确认：验证只走 Portainer 官方签名协议，不做额外的 token/Bearer 通道。这套机制是上游 `crypto/ecdsa.go` 的既有行为，不改：

- Portainer Server 每个请求带两个头：`X-PortainerAgent-PublicKey`（hex 编码的 ECDSA 公钥）和 `X-PortainerAgent-Signature`（base64 无填充，长度为 2×密钥字节数，前半 r、后半 s）。
- `AGENT_SECRET` 就是共享凭据本身，等价于“token”：secret 模式下被签名的内容是 `MD5(AGENT_SECRET)`（默认模式签的是固定消息 `Portainer-App`）。Portainer 添加环境时填的 Secret 与它同值，除此之外不设第二把密钥。
- 缺任一头或验签失败 → 403。`/ping` 是唯一例外（204、无签名）。
- 不做 `Authorization: Bearer` 备用通道：标准 Portainer 不会发 token，加了只是扩大攻击面。curl 直连测试用签名工具（见测试节的 testhelper）。
- 签名验证发生在 `http/handler/docker` 的 notary 中间件里，wslc 后端收到的请求都已验过签，不需要重复校验。

### Windows 服务协议

`cmd/agent` 是普通控制台程序。直接 `New-Service -BinaryPathName` 指向它，进程不调用 SCM 的 `StartServiceCtrlDispatcher`，约 30 秒后被 SCM 以“服务未响应”杀掉。**必须加服务包装**：

- 在 `cmd/agent` 里（仅 Windows 构建）用 `golang.org/x/sys/windows/svc`：`svc.IsWindowsService()` 为真时走 `svc.Run`，把 SCM 的 Stop 事件转成 context 取消，交给 `http.Server.Shutdown`；否则按普通进程跑，方便开发调试。`x/sys` 已在依赖图里（go-winio 引它），不算新依赖。
- Stop 时必须终止所有流式子进程：`logs -f`、`events`、拉取中的 `pull`。`RunStreaming` 的 ctx 取消 → `cmd.Process.Kill`。
- 开发期两种跑法：控制台直接运行；服务方式运行。二者行为一致，便于排查“session 0 差异”（见风险一节）。

### 环境变量

沿用官方变量，不另做 `config.yml`：

| 变量 | 第一版 |
| --- | --- |
| `WSLC` | `1` |
| `WSLC_PATH` | 可选。默认 `C:\Program Files\WSL\wslc.exe`，再用 `PATH` 查找 |
| `WSLC_SESSION` | 可选。对应全局 `--session`，默认会话 `0` |
| `AGENT_SECRET` | 必填 |
| `AGENT_PORT` | `9001` |
| `AGENT_HOST` | `0.0.0.0` |
| `LOG_LEVEL` | `INFO` 或 `DEBUG` |

Edge 相关变量不要设。

## wslc 输出契约

### 两种 JSON，不要混用

`wslc ps`、`wslc images`、`wslc stats`、`wslc events` 的 `--format json` 是 **NDJSON**：一行一个对象，不是数组。空结果是空 stdout，不是 `[]`。

`wslc inspect` 默认是缩进的 **JSON 数组**。加 `--format json` 变成一行。Docker 的 inspect 接口要的是单个对象，必须脱掉外层数组。

列表命令一律加 `--no-trunc`。不加时容器 ID 只有 12 位，命令行和挂载路径会被截断。

### 容器列表：`wslc ps -a --format json --no-trunc`

实测一行（节选）：

```json
{
  "ID": "ac3d3bff1501b395a571f2cde1f81e3f30514e77430f0b9522dd5fc4db672d39",
  "Names": "wild_flinders",
  "Image": "ghcr.nju.edu.cn/ggml-org/llama.cpp:server-cuda",
  "Command": "\"/app/llama-server -m /models/... \"",
  "CreatedAt": "2026-10-22 18:37:10 +0800 GMT+8",
  "State": "exited",
  "Status": "Exited (255) 26 minutes ago",
  "Ports": "",
  "Labels": "com.microsoft.wsl.container.metadata={...},maintainer=...",
  "Networks": "bridge",
  "Mounts": "/mnt/{56441389-10b0-4389-9300-f941db46e8c6}",
  "Size": "0B",
  "Platform": { "architecture": "amd64", "os": "linux" }
}
```

这条输出不能直接映射成 Docker 列表：

- `CreatedAt` 的布局是 `2006-01-02 15:04:05 -0700 MST`，不是 RFC3339。
- `Names` 是单个字符串，没有前导 `/`。
- `Ports` 在已发布 `127.0.0.1:9080->8080` 的容器上仍是 `""`。
- `Labels` 是逗号拼接的字符串，值里还有 JSON。不能按逗号拆。
- `Command` 两端带着转义引号，直接透传会在 Portainer 列表里显示成 `"/app/llama-server …"`。

所以 `GET /containers/json` 的做法是：

1. `wslc ps`（带不带 `-a` 看查询参数 `all`）拿 ID、`Status`、`State`。
2. `Command` 不取 ps 的，用 inspect 的 `Config.Entrypoint + Config.Cmd` 拼成 Docker 风格的命令字符串。
3. 把这些 ID **一次**传给 `wslc inspect`（它接受多个 ID），补端口、标签、挂载、网络、创建时间。避免 N+1 次进程启动。
4. 批量 inspect 失败时降级为逐个 inspect：容器正在被删时单个 ID 会失败，跳过该 ID 并记日志，不让整个列表 500。
5. 没有容器时直接返回 `[]`，不要再调 inspect。
6. `limit`、`size`、`filters` 等其他查询参数接受并忽略，不报 400。

### 容器 inspect：已经接近 Docker

`wslc inspect --type container <id>` 返回数组，元素的骨架和 Docker ContainerJSON 相同。实测有：

- `Id`：64 位十六进制，没有 `sha256:` 前缀
- `Name`：已带前导 `/`，例如 `"/wild_flinders"`
- `Image`：`sha256:…`
- `Created`：RFC3339Nano，例如 `2026-10-22T10:37:10.144254856Z`
- `Config`：`Cmd`、`Entrypoint`、`Env`、`Labels`（对象）、`WorkingDir`、`User`、`Image`（仓库:标签）
- `HostConfig`：`Memory`、`NanoCpus`、`NetworkMode`、`Ulimits`。没有 `PortBindings`、`Binds`
- `State`：`Status`、`Running`、`ExitCode`、`StartedAt`、`FinishedAt`、`Health`。没有 `Pid`、`OOMKilled`、`Error`
- `Mounts[]`：`Type`（`bind`）、`Source`（Windows 路径 `D:\\models`）、`Destination`、`ReadWrite`、`Name`
- `NetworkSettings.Networks.<name>`：`IPAddress`、`Gateway`、`MacAddress`、`IPPrefixLen`
- 顶层 `Ports`：Docker 的端口映射表，例如 `"8080/tcp": [{"HostIp":"127.0.0.1","HostPort":"9080"}]`

Docker 把这张表放在 `NetworkSettings.Ports` 和 `HostConfig.PortBindings`。转换时把顶层 `Ports` 复制到这两处，再从响应里去掉顶层 `Ports`。缺的字段用零值补上，让 Portainer 的详情页能反序列化。

`Created` 在 inspect 里保持字符串。只有列表接口才改成 Unix 秒。

### 镜像列表和镜像 inspect

`wslc images --format json --no-trunc` 仍是一行一个对象：

```json
{
  "ID": "sha256:9806c8d385d34d4806d19be5edd046aeeb5e1a923cb55a07ea043fc19c97592a",
  "Repository": "ghcr.nju.edu.cn/ggml-org/llama.cpp",
  "Tag": "server-cuda",
  "Digest": "<none>",
  "CreatedAt": "2026-10-22 13:45:22 +0800 GMT+8",
  "Size": "4.36GB",
  "Containers": "1"
}
```

同一 ID 有多个 tag 时会有多行。按 ID 合并成一个 Docker 镜像对象，`RepoTags` 为 `["repository:tag"]`。`Repository` 或 `Tag` 是 `<none>` 时不要拼进 `RepoTags`。

`Size` 是四舍五入后的文本（`4.36GB`），不能用。同一镜像 `wslc inspect --type image` 的 `Size` 是整数 `4356415882`。列表接口把所有 ID **一次**传给 `wslc inspect --type image` 批量取字节数，`VirtualSize` 填同一个值，`Containers` 从字符串转整数。批量失败同样逐个降级。

镜像 inspect 的其余字段（`RepoTags`、`RepoDigests`、`Config`、`RootFS`、`Architecture`、`Os`、`Created`）已经是 Docker 形态，inspect 接口基本可以直接透传（去掉外层数组）。

### stats 只有快照

`wslc stats --all --format json --no-trunc` 没有 follow，没有周期输出。实测：

```json
{
  "ID": "ac3d3bff1501b395a571f2cde1f81e3f30514e77430f0b9522dd5fc4db672d39",
  "Name": "wild_flinders",
  "CPUPerc": "0.00%",
  "MemUsage": "0B / 0B",
  "MemPerc": "0.00%",
  "NetIO": "0B / 0B",
  "BlockIO": "0B / 0B",
  "PIDs": 0
}
```

Docker 的 `GET /containers/{id}/stats?stream=1` 是连续的 StatsJSON。Portainer 用**相邻两个样本**算 CPU：`(cpu_stats.cpu_usage.total_usage - precpu_stats.cpu_usage.total_usage) / (cpu_stats.system_cpu_usage - precpu_stats.system_cpu_usage) × online_cpus × 100`。

做法：

- 维护一个合成计数器：`system_cpu_usage` 每秒加 `NCPU × 1e9`（即主机满负荷 CPU 时间）；`total_usage` 每秒加 `CPUPerc/100 × NCPU × 1e9`。这样 Portainer 的公式还原出 wslc 的百分比。
- **每个样本都要带 `precpu_stats`（上一份合成样本）**，否则前端算不出 CPU。第一个样本的 precpu 取全零。
- `read` / `preread` 填采样时刻的 RFC3339Nano。
- `memory_stats.usage / limit` 来自 `MemUsage` 的 `"used / limit"`；`networks` 和 `blkio_stats` 分别由 `NetIO`、`BlockIO` 填。
- 对已停止的容器，`wslc stats <id>` 可能报错。此时合成一份全零样本返回（Docker 对停止容器就是返回零值 stats），不要 500。
- `stream=0` 打一次；`stream=1` 每秒打一次直到客户端断开。

这是展示用的近似值，不是 cgroup 原始计数。

### events 还没有样本

`wslc events --format json --since … --until …` 在本机这段时间里没有输出。转换函数先留空，实现时启动一个容器或执行 start/stop，把一行原始 JSON 记进 `wslc/testdata/event.json`，再写映射。在那之前不要猜字段名。

Docker 事件是一行一个对象：`Type`、`Action`、`Actor.ID`、`Actor.Attributes`、`time`、`timeNano`、`scope`。

### 时间布局

按这个顺序解析：

| 布局 | 例子 | 出现在 |
| --- | --- | --- |
| `2006-01-02 15:04:05 -0700 MST` | `2026-10-22 18:37:10 +0800 GMT+8` | `ps`、`images` 的 `CreatedAt` |
| `time.RFC3339Nano` | `2026-10-22T10:37:10.144254856Z` | inspect 的 `Created`、`StartedAt`、`FinishedAt` |
| `2006-01-02 15:04:05.000000000 -0700 MST` | `2026-11-24 05:46:33.255274614 +0000 UTC` | 网络列表。第一版不用，解析器保留 |

## 第一版端点

wslc handler 在路由前自己剥 `/v1.xx` 前缀。查询参数按 Docker Engine API 读取；未列出的参数接受并忽略。

| Docker API | wslc | 响应要点 |
| --- | --- | --- |
| `GET /_ping`（含 HEAD） | 不调用 | `200`，body `OK`，头 `Api-Version: 1.44` |
| `GET /version` | 解析 `wslc info` 文本 | `Version` 用会话管理器版本 `3.0.3`，`ApiVersion`/`MinAPIVersion` 为 `1.44`，`Os=linux`，`Arch=amd64`，`KernelVersion` 取内核行 |
| `GET /info` | `wslc info` 加 `ps -a` 和 `images` 计数 | 见下方字段来源 |
| `GET /containers/json` | `ps`，然后一次批量 `inspect` | JSON 数组。`Created` 为 Unix 秒，`Names` 为 `["/name"]`，`State` 小写，`Status` 用 ps 的相对时间，`Command` 用 inspect 拼，`Ports` 来自 inspect |
| `GET /containers/{id}/json` | `inspect --type container` | 单个对象，不是数组 |
| `POST /containers/{id}/start` | `wslc start <id>` | `204` |
| `POST /containers/{id}/stop` | `wslc stop -t <秒> <id>` | 查询参数 `t`；不传就不带 `-t`，让 wslc 用容器自己的超时。`204` |
| `POST /containers/{id}/restart` | `wslc restart -t <秒> <id>` | 同上。Portainer 容器菜单有 Restart，缺了会 501 |
| `POST /containers/{id}/kill` | `wslc kill -s <signal> <id>` | 查询参数 `signal`，不传用 wslc 默认 SIGKILL。`204` |
| `POST /containers/{id}/pause`、`/unpause` | — | wslc 没有 pause。501 |
| `DELETE /containers/{id}` | `wslc rm` | `force=1` 加 `-f`；**`v=1` 加 `-v`**（wslc 支持，删匿名卷）。`204` |
| `GET /containers/{id}/logs` | `wslc logs` | 见下文 |
| `GET /containers/{id}/stats` | `wslc stats --format json --no-trunc <id>` | 见上文 |
| `GET /images/json` | `images`，然后一次批量 `inspect --type image` | 按 ID 合并 tag。`Created` 为 Unix 秒，`Size` 用 inspect 的整数 |
| `GET /images/{name}/json` | `inspect --type image` | 脱数组透传。Portainer 镜像详情要用 |
| `POST /images/create` | `wslc pull <fromImage>:<tag>` | 查询参数 `fromImage`、`tag`。见下文 |
| `DELETE /images/{name}` | `wslc rmi` | `force=1` 加 `-f`。body 在实测 rmi 输出前先返回 `[]`（Docker 合法响应），实测后再补 Untagged/Deleted |
| `GET /events` | `wslc events --format json` | 长连接。映射等拿到真实样本再写 |

`GET /info` 和 `GET /version` 必须先可用。Portainer 添加环境时会先打它们。

`GET /info` 的字段来源：

- `Containers*` 四个计数：`ps -a --format json` 数出来，不解析 info 文本
- `Images`：`images --format json` 按 ID 去重后计数
- `NCPU`：`runtime.NumCPU()`（Agent 就跑在这台机器上）
- `MemTotal`：`GlobalMemoryStatusEx`（`golang.org/x/sys/windows`）
- `Name`：Windows 主机名
- `ServerVersion`：`wslc info` 的会话管理器版本
- `OSType=linux`、`Driver=wslc`、`Architecture=amd64`、`DockerRootDir=d:\wslc-data`（来自 settings.yaml 的 storagePath）
- `Swarm.LocalNodeState=inactive`（Portainer 由此判定 standalone）

### 日志帧

`wslc logs` 是纯文本。Portainer 在容器没有 TTY 时按 Docker multiplexed stream 解析。每一帧 8 字节头加负载：

```
byte 0   1 = stdout，2 = stderr
byte 1-3 0
byte 4-7 负载长度，大端 uint32
```

wslc 不区分 stdout 和 stderr，全部标成 stdout。一行一个帧，保留换行。

查询参数：

| 参数 | wslc |
| --- | --- |
| `follow=1` | `-f`。流式子进程不受普通 60 秒超时约束；但服务器 `WriteTimeout` 30 分钟会掐断长连接，客户端会重连，行为与官方 Agent 一致 |
| `timestamps=1` | `-t` |
| `tail` | `--tail`。`tail=all` 时不传 |
| `since`、`until` | Docker 可能发**带小数的 Unix 秒**（如 `1730000000.123`）或 RFC3339；wslc 只收整数秒或 RFC3339。先归一化（去小数或转 RFC3339）再传 `--since` / `--until` |
| `stdout=0` 且 `stderr=0` | 不调 wslc，返回空 body |

`Content-Type` 用 `application/vnd.docker.raw-stream`。

### 拉取的进度流

`POST /images/create` 的 body 是一行一个 JSON。Portainer 只要求每行能解出 `status`。实现时先抓一次 `wslc pull` 的真实 stdout：

- 如果已经是 JSON 行，转成 Docker 的 `status` / `progressDetail`
- 如果是文本，每一行包装成 `{"status":"<该行>"}`

不要在看到真实输出之前写进度条解析。拉取进程的超时至少 30 分钟，客户端断开时杀掉子进程。

私有仓库：Portainer 拉取时会带 `X-Registry-Auth` 头。第一版不解它——需要私有镜像时，先在这台机器上手动 `wslc login`（凭据存在用户的凭据管理器）。文档里写明这个限制。

### 兼容用的空响应

Portainer 打开环境时还会请求 `GET /networks` 和 `GET /volumes`。这两个不在第一版能力里。为了让页面能打开，它们返回空集合，而不是 501：

- `GET /networks` → `[]`
- `GET /volumes` → `{"Volumes":[],"Warnings":null}`

`POST`、`DELETE` 以及其他未列出的路径返回 501，body 为 Docker 错误对象 `{"message":"wslc agent: not implemented: METHOD /path"}`。

找不到对象时返回 404，`message` 带上 wslc 的 stderr。其余非零退出码返回 500。

## wslc 包

新代码放在 fork 的 `wslc/` 下，导入路径 `github.com/portainer/agent/wslc`。

| 文件 | 职责 |
| --- | --- |
| `exec.go` | 查找 `wslc.exe`，执行参数，分开返回 stdout、stderr、退出码。普通命令超时 60 秒。`RunStreaming` 不设超时，返回 stdout pipe，ctx 取消时杀进程。`--session` 存在时拼在子命令前 |
| `ndjson.go` | 按行解码。空输入得到空切片，不报错 |
| `time.go` | 上一节的三种布局，加 `since` 参数归一化 |
| `handler.go` | `http.Handler`。先剥 `/v1.xx`，再用标准库 `http.ServeMux`（Go 1.22 方法+路径）分发上表 |
| `containers.go` | 列表、inspect、start、stop、restart、kill、rm |
| `logs.go` | 日志帧 |
| `images.go` | 列表、inspect、pull、rmi |
| `stats.go` | 快照、合成计数器、每秒轮询 |
| `events.go` | 事件流。样本落地前，连接保持打开并只转发已经能解析的行 |
| `info.go` | 解析 `wslc info` 的中文文本：`WSL 版本`、`内核版本`、`Windows 版本`、`会话管理器版本`、`会话`。另含 `NCPU`/`MemTotal` 采集 |
| `testdata/` | 把本次实测的 ps 行、inspect、images 行、stats 行收成文件，单测只读这些文件 |
| `testhelper/` | 测试用独立 main（不进 agent 构建）：对 `MD5(AGENT_SECRET)` 生成 ECDSA 签名头并发请求。见测试节 |

`LocalProxy` 的改动保持很小：

```go
func (proxy *LocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if proxy.wslc != nil {
        proxy.wslc.ServeHTTP(w, r)
        return
    }
    // 原有的命名管道 / Unix socket RoundTrip 保留，供非 WSLC 构建使用
}
```

`NewLocalProxy` 在 `WSLC=1` 时填上这个 handler，不再拨 `docker_engine`。

## 并发与会话生命周期

- Portainer 容器页开着时每几秒轮询一次列表。每次列表 = `ps` + 一次批量 `inspect`。不要为省进程加缓存，宁可多起进程，避免列表和真实状态不一致。
- 轮询的副作用：会话 VM 几乎不会走到 30 秒空闲关停。这是预期行为，不是泄漏。写进运维说明。
- `ps`、`inspect`、`start/stop` 等短命令可以并发起进程（wslc.exe 自己处理会话互斥）。唯一要串行的是冷启动期：VM 正在起时并发请求各自阻塞即可，Go 侧不额外加锁。
- `logs -f`、`events`、`pull` 三个流式进程数要有上限（建议同时 16 个），超过返回 429。防止 Portainer 侧异常重连把进程表打爆。

## 构建和安装

在 `agent/` 目录：

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o dist\agent-wslc.exe .\cmd\agent
```

安装脚本做五件事：复制 exe 到 `%ProgramData%\PortainerAgentWslc`（即服务工作目录，证书写在这里）、建防火墙入站 TCP 9001、创建服务（账户见下一节）、把环境变量写进服务注册表、启动服务。

`New-Service` 不会带环境变量，要写 `HKLM\SYSTEM\CurrentControlSet\Services\PortainerAgentWslc` 的 `Environment`（`REG_MULTI_SZ`）：

```
WSLC=1
AGENT_SECRET=<与 Portainer 里填写的相同>
AGENT_PORT=9001
AGENT_HOST=0.0.0.0
LOG_LEVEL=INFO
```

服务名 `PortainerAgentWslc`，启动类型自动。

## 跑在哪个 Windows 用户下

wslc 的设置在用户目录 `%LOCALAPPDATA%\wslc\`，容器和镜像属于该用户的会话。服务若以 `LocalSystem` 运行，会看到另一个空会话。

第一版把服务配成 wslc 所属用户（本机是 `zhangwj50`），并授予“作为服务登录”权限。不要用 `LocalSystem`。

有一个**未验证的假设**：服务在 session 0（非交互会话）里以该用户身份跑 `wslc.exe`，能否连上用户交互会话里的 wslc 会话 VM。实施顺序里把这件事列为第 3 步的验收条件。如果不行，回退方案：不用服务，改用“登录时启动”的计划任务（`/SC ONLOGON`，运行在该用户会话里），代价是机器重启后必须有人登录才会启动。

会话空闲约 30 秒后会关停 VM。Agent 的第一次 `ps` 或 `inspect` 可能要等 VM 冷启动，所以普通命令超时用 60 秒。

## Portainer Server 放哪

两种拓扑，安装节按情况选：

1. **远程**：Portainer Server 跑在另一台机器（Docker 或其他），地址填 `https://<这台机器的 IP>:9001`。此时 Agent 绑 `0.0.0.0`，防火墙放行 9001。
2. **同机**：Portainer Server 自己也用 wslc 跑在这台机器上：

   ```powershell
   wslc run -d --name portainer -p 9443:9443 `
     -v C:\portainer-data:/data portainer/portainer-ce:latest
   ```

   环境地址填 `https://host.wslc.internal:9001`（wslc 内置的宿主回环域名，settings.yaml 里 `hostLoopback` 可改）。注意 settings.yaml 的 `defaultBindingAddress` 默认 `127.0.0.1`：容器端口默认只绑回环，要让局域网访问 9443 就要 `-p 0.0.0.0:9443:9443` 或改设置。

Portainer 里添加环境：

- 类型：Agent
- 地址：见上
- Secret：与 `AGENT_SECRET` 相同
- 打开跳过 TLS 校验（证书是 Agent 启动时签的）

先在本机访问 `https://127.0.0.1:9001/ping` 确认 204，再从 Portainer 添加。`/ping` 不需要签名；`/info` 和 `/containers/json` 没有合法签名时必须是 403。添加完成后 `/agents` 返回 503 属于正常（standalone 无集群）。

## 测试

单测不启动 wslc，只用 `wslc/testdata`：

- NDJSON 一行、多行、空输入
- `CreatedAt` 的 `+0800 GMT+8` 能解析，并转成正确的 Unix 秒
- ps 的空 `Ports` 不会被当成数组；inspect 的顶层 `Ports` 会进 `NetworkSettings.Ports` 和 `HostConfig.PortBindings`
- inspect 数组会解开成单个对象
- `Command` 从 Entrypoint+Cmd 拼接，ps 里的引号不透传
- 镜像多行按 ID 合并；`4.36GB` 不作为 `Size`，inspect 的 `4356415882` 才是
- 日志一行生成 8 字节头，长度等于负载
- `since=1730000000.123` 归一化成 wslc 可接受的参数
- stats：`CPUPerc=12.50%` 时，两个相邻合成样本经 Portainer 公式得到 12.5；停止容器得到全零样本
- 未知路径返回 501 和 `{"message":…}`；`/v1.44/containers/json` 与 `/containers/json` 路由等价

`/ping` 之外的端点都需要合法签名，裸 curl 只会 403。为此加 `wslc/testhelper`：输入 `AGENT_SECRET`，生成临时 P-256 密钥对、对 `MD5(secret)` 签名，带着 `X-PortainerAgent-PublicKey` / `X-PortainerAgent-Signature` 两个头发请求。自检标准：它产出的签名能被 `crypto.NewECDSAService(secret).VerifySignature` 接受。

集成测试在这台机器上跑，一律经 testhelper 或 Portainer 发请求。针对已有对象只做读：`/containers/json`、`/containers/{id}/json`、`/images/json`、`/info`、`/version`。start、stop、restart、kill、rm、pull、rmi 用专门的测试镜像，不碰 `wild_flinders`。

Portainer 验收只看这几项：

- 环境能加上，类型显示为 Docker standalone，不显示 Swarm，也不被当成 Kubernetes
- 容器列表有名称、镜像、状态、端口 `9080:8080`
- 详情里能看到环境变量、挂载 `D:\models -> /models`、网络 IP
- 启动、停止、重启、强制停止、删除可用
- 日志能看，follow 能停
- 镜像列表有大小和 tag；能拉取、能删除
- 容器统计页有 CPU、内存曲线
- 对容器做 start/stop 时，事件流里出现对应事件
- `/browse/ls` 返回 404

## 不在第一版

| 能力 | 原因 |
| --- | --- |
| `POST /containers/create` 及部署流程 | 未纳入范围。Portainer 上的“添加容器/部署 stack”会 501 |
| exec、attach、`/websocket/exec` | 未纳入范围 |
| 网络、卷的创建和删除 | 只返回空列表，避免页面报错 |
| Swarm、Edge、Kubernetes | `clusterService == nil`，相关代码不改 |
| 私有仓库拉取（`X-Registry-Auth`） | 需要宿主机先 `wslc login`；解 auth 头留给第二版 |
| WSLC SDK | 本机没有 DLL 和头文件 |

## 风险

- **session 0 假设未验证。** 服务上下文里 `wslc.exe` 能否触达用户会话 VM 是整个方案最大的未验证点，列为实施第 3 步的验收条件；不行就退到登录时启动的计划任务。
- **列表成本。** 每次列表 = `ps` + 一次批量 `inspect`。Portainer 轮询下进程启动频繁但可接受；不做缓存，避免状态不一致。
- **stats 是近似值。** CPU 曲线能看，不能和 `docker stats` 的原始计数对比。
- **事件映射未验证。** 没有样本之前事件页可能是空的。这是第一版里唯一允许后补的转换。
- **中文 stderr。** 错误文本可以放进 `message`，分支逻辑只用退出码。
- **用户绑定。** 服务账户和 wslc 数据不是同一个用户时，Portainer 里会是一台“空 Docker”。安装脚本要检查服务账户的 `LOCALAPPDATA\wslc` 是否存在。
- **长连接 30 分钟被掐。** 官方 Agent 的 `WriteTimeout` 行为，`logs -f` 会断，客户端重连即可。不要为此改服务器超时。
- **上游合并。** 改动限制在 `main.go` 的启动分支、`LocalProxy`、`handler.go` 的 `/browse` 接线和新增的 `wslc/` 包。

## 实施顺序

1. 把 `portainer/agent` 克隆到 `d:\work\wslc\agent`，记录 `develop` 的 commit，建分支 `feat/wslc-backend`。
2. 把本文的实测输出存进 `wslc/testdata`，先写解析和转换的单测。
3. 加 `WSLC=1` 启动分支（含工作目录、`ContainerPlatform`、svc 包装），先在**控制台**跑通：进程不碰 Docker 管道，听 9001，`/ping` 返回 204。
4. **session 0 验证**：把同一个 exe 装成服务启动，在服务上下文里调 `/containers/json` 能看到 `wild_flinders`。不行，切到计划任务方案，再继续。
5. 先写 `wslc/testhelper`（签名头生成），再接通 `/_ping`、`/version`、`/info`、容器列表和 inspect。对自签 HTTPS 做带签名的读验证（裸 curl 只有 `/_ping` 能通）。
6. 接通 start、stop、restart、kill、rm、镜像列表、镜像 inspect、rmi、pull。
7. 接通日志帧、stats 轮询。
8. 做一次 start/stop，录下 `wslc events --format json` 的一行，再完成事件转换。
9. 用 Portainer 按上一节的验收表走一遍。
