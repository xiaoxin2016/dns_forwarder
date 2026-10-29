# dns_forwarder

一个用 Go 编写的简单 DNS 服务器：

- 读取程序同目录下的 `hosts` 文件（Linux hosts 格式），本地应答 A / AAAA 记录，支持泛域名 `*.example.com`；
- 不在 `hosts` 中的查询转发给 `dns.txt` 中列出的上游 DNS（每行一个 IP），按顺序尝试、失败自动切换；
- 运行时（Windows）自动记录本机各网卡当前的 DNS 设置并改为 `127.0.0.1` / `::1`，正常退出时改回原值。

## 使用

1. 从 [Releases](https://github.com/xiaoxin2016/dns_forwarder/releases) 下载对应架构的 zip 并解压；
2. 按需编辑同目录下的 `hosts` 与 `dns.txt`；
3. **以管理员身份**运行 `dns_forwarder.exe`（修改系统 DNS 需要管理员权限；没有权限时程序只提供 DNS 服务，不修改系统设置）；
4. 按 `Ctrl+C` 或关闭控制台窗口退出，程序会自动恢复原来的 DNS 设置。

修改 `hosts` / `dns.txt` 后无需重启，程序每 3 秒检查一次并自动重新加载；新配置有误时保留旧配置。

### hosts 格式

```
# IP          域名 [别名...]        # 注释
192.168.1.10  nas.lan
192.168.1.20  example.com *.example.com
2001:db8::1   v6.lan
```

匹配规则：

- 域名不区分大小写；
- 精确记录优先于泛域名；
- `*.example.com` 匹配任意层级子域名（`a.example.com`、`a.b.example.com`），**不**匹配 `example.com` 本身；
- 多条泛域名同时命中时，最长（最具体）的后缀优先，例如 `*.dev.example.com` 优先于 `*.example.com`；
- 同一域名可写多行，地址会合并；
- 命中 hosts 的域名：A / AAAA 查询由本地应答，若没有对应地址族的记录则返回空的 NOERROR（不会被上游结果覆盖）；其他类型（MX、TXT 等）仍然转发上游。

### dns.txt 格式

```
# 每行一个上游，可带端口
223.5.5.5
119.29.29.29
1.1.1.1:53
[2001:4860:4860::8888]:53
```

指向本机自身（如 `127.0.0.1`，端口与监听端口相同）的上游会被忽略，以避免转发环路。

### 系统 DNS 自动切换（Windows）

启动时：

1. 枚举所有已启用、非回环的网卡，从注册表读取每块网卡 IPv4 / IPv6 的静态 DNS（为空表示"自动获取 / DHCP"）；
2. 把原始设置写入同目录的 `dns_backup.json`；
3. 通过 `netsh` 把 IPv4 DNS 设为 `127.0.0.1`、IPv6 DNS 设为 `::1`，并执行 `ipconfig /flushdns`。

退出时（`Ctrl+C`、关闭控制台窗口、注销 / 关机）按备份恢复：原来是 DHCP 的恢复为自动获取，原来是静态的恢复为原静态地址列表，成功后删除 `dns_backup.json`。

如果程序被强制结束（任务管理器结束进程、崩溃、断电），系统 DNS 会停留在 `127.0.0.1`。此时：

- 再次启动程序：检测到遗留的 `dns_backup.json` 时会把其中记录的设置视为原始值（不会把 `127.0.0.1` 误记为原值），正常退出时即可恢复；
- 或者直接执行 `dns_forwarder.exe -restore`，只恢复 DNS 后退出。

不需要切换系统 DNS 时使用 `-setdns=false`。只有监听地址覆盖 `127.0.0.1:53` 时才会切换（如 `-listen :53`、`-listen 127.0.0.1:53`）。

### 命令行参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-listen` | `:53` | 监听地址（UDP + TCP） |
| `-hosts` | 程序同目录 `hosts` | hosts 文件路径 |
| `-upstream` | 程序同目录 `dns.txt` | 上游列表路径 |
| `-ttl` | `60` | hosts 应答的 TTL（秒） |
| `-timeout` | `2s` | 单个上游的查询超时 |
| `-reload` | `3s` | 配置文件变更检查间隔，`0` 关闭 |
| `-setdns` | `true` | 运行期间把系统 DNS 指向本程序（仅 Windows） |
| `-restore` | | 按 `dns_backup.json` 恢复系统 DNS 后退出 |
| `-v` | | 打印每条查询日志 |
| `-version` | | 打印版本号 |

默认先在程序所在目录查找 `hosts` / `dns.txt`，找不到时再查找当前工作目录（便于 `go run .`）。

## 构建

```bash
go test ./...
GOOS=windows GOARCH=amd64 go build -o dns_forwarder.exe .
```

## 发版

推送 `v` 开头的 tag，或在 Actions 页面手动运行 Release 工作流并填写版本号（如 `v1.0.0`，会在所选分支的最新提交上创建该 tag），即触发 GitHub Actions（`.github/workflows/release.yml`）：运行测试，编译 Windows amd64 / 386 / arm64 版本，打包为 zip（含 exe、`hosts`、`dns.txt`、README）并创建 GitHub Release。

```bash
git tag v1.0.0
git push origin v1.0.0
```
