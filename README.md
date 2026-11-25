# 项目概述
- 纯 CLI 工具（Go 编写），用于自动化执行 apt update -y && apt full-upgrade -y

# 核心功能与技术要求
- **以 root 身份执行**： 假若当前不是 root \ sudo ,那么退出执行，并输出提示信息。
- **自动化执行**：
 - 每次启动检查 apt_upgrade 创建 apt_upgrade.log 文件，若已有，直接清空此文件
 - apt update -y && apt full-upgrade -y
 - 检查 apt full-upgrade -y 的输出日志，假若软件包更新涉及 (不区分大小写) liux kernel,linux image,ssl,ssh,nginx 等需要 reboot 的则 执行 reboot
 - 指令执行的显示信息，都输出到标准输出，并同时追加写入 apt_upgrade.log ，行首附带时间戳，使用 UTC 时区，时间戳结尾标明 UTC
- **处理进度与状态显示**：处理过程中，控制台实时显示带有运行状态转圈动画（如 `⠋`）

# 编译与交叉编译指南
在 Go 1.27 环境下编译前，如根目录下无 `go.mod`，需先生成 `go.mod`：

通过 `go build` 构建，建议设置 `CGO_ENABLED=0` 实现静态编译：

- **Linux 64-bit Intel**:
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o apt_upgrade_linux_amd64 src/main.go
  ```
- **Linux ARM 64-bit**:
  ```bash
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o apt_upgrade_linux_arm64 src/main.go
  ```

# Go 模块文件说明 (`go.mod` 与 `go.sum`)
- **`go.mod`**：定义模块名称（`apt_upgrade`）及声明外部依赖（如 `github.com/rwcarlsen/goexif` 和 `golang.org/x/image`）。
- **`go.sum`**：记录所有依赖包及其特定版本的加密校验和（Cryptographic Checksums），用于防止依赖被篡改并确保团队与 CI 构建的一致性。

## 已实现功能（2026-11-25）
- 目录结构：
  - `go.mod`：Go 模块定义，无外部依赖
  - `src/main.go`：命令行工具业务代码
  - `src/main_test.go`：单元测试
  - `logs.md`：操作记录（UTC 时间戳）
- 运行要求：Debian / Ubuntu，且必须为 root（或使用 `sudo`）；非 root 时提示并退出。
- 执行流程：
  1. 在可执行文件同目录创建或清空 `apt_upgrade.log`
  2. 执行 `apt update -y`
  3. 执行 `apt full-upgrade -y`
  4. 若升级输出命中 `linux kernel`、`linux image`、`ssl`、`ssh`、`nginx`（不区分大小写，兼容 `linux-image` 写法），或 `/var/run/reboot-required` 存在，则执行 `reboot`
  5. 命令输出与状态信息同时写入标准输出和 `apt_upgrade.log`，每行行首为 UTC 时间戳、行尾标注 `UTC`
- 转圈动画：只写入当前控制终端 `/dev/tty`，不写入日志、标准输出或标准错误；无终端时自动跳过。
- 命令执行时设置 `DEBIAN_FRONTEND=noninteractive`，避免交互提示阻塞升级。

## 运行与测试
```bash
# 运行单元测试
go test ./...

# 静态检查
go vet ./...

# 编译（产物不会纳入版本控制）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o apt_upgrade_linux_amd64 src/main.go
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o apt_upgrade_linux_arm64 src/main.go

# 以 root 运行（请勿在测试机器上执行，可能触发重启）
sudo ./apt_upgrade_linux_amd64
```

