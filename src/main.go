package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	logFileName     = "apt_upgrade.log"
	rebootFlagPath  = "/var/run/reboot-required"
	timeLayout      = "2006-01-02 15:04:05"
	spinnerInterval = 100 * time.Millisecond
	// 固定的安全 PATH，避免从 root 环境变量继承被篡改的搜索路径。
	safePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// 升级输出中需要重启的软件包关键字（不区分大小写）。
var rebootKeywords = []string{"linux kernel", "linux image", "ssl", "ssh", "nginx"}

// URL 中的用户名密码或令牌，写入日志前需要脱敏。
var urlCredentialPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)

// 转圈动画的逐帧字符。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// commandRunner 抽象命令执行，便于单元测试替换成假实现。
type commandRunner interface {
	Run(name string, args ...string) (string, error)
}

// commandPaths 保存命令的绝对路径，避免依赖不可信的 PATH 查找。
type commandPaths struct {
	apt    string
	reboot string
}

// realRunner 在真实的 Debian / Ubuntu 系统中执行命令。
type realRunner struct{}

func (realRunner) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = commandEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// commandEnv 返回最小化的子进程环境，避免把 root 的敏感变量传给 apt。
func commandEnv() []string {
	return []string{
		"PATH=" + safePath,
		"LANG=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}
}

// resolveCommand 从绝对路径候选中选出第一个可执行文件。
func resolveCommand(candidates ...string) (string, error) {
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return path, nil
	}
	return "", errors.New("找不到可执行命令：" + strings.Join(candidates, "、"))
}

// isRoot 判断 euid 是否为 root（使用 sudo 时 euid 也是 0）。
func isRoot(uid int) bool {
	return uid == 0
}

// logFilePath 返回可执行文件所在目录下的日志路径。
func logFilePath(executable func() (string, error)) (string, error) {
	path, err := executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), logFileName), nil
}

// prepareLogFile 创建日志文件；文件已存在时清空内容。
// 使用 O_NOFOLLOW 防止符号链接攻击，权限设为仅属主可读写。
func prepareLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
}

// formatLogLine 生成一行带 UTC 时间戳的日志。
func formatLogLine(now time.Time, text string) string {
	return now.UTC().Format(timeLayout) + " UTC | " + text
}

// redactSensitive 把 URL 中的账号密码或令牌替换成掩码。
func redactSensitive(text string) string {
	return urlCredentialPattern.ReplaceAllString(text, "${1}***@")
}

// writeLog 把文本逐行脱敏并加上时间戳后写入目标。
func writeLog(w io.Writer, now time.Time, text string) error {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			continue
		}
		if _, err := io.WriteString(w, formatLogLine(now, redactSensitive(line))+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// multiLogger 把同一份内容写入多个目标（标准输出和日志文件）。
type multiLogger struct {
	writers []io.Writer
	now     func() time.Time
}

func (l *multiLogger) Write(text string) error {
	for _, w := range l.writers {
		if err := writeLog(w, l.now(), text); err != nil {
			return err
		}
	}
	return nil
}

// normalizeForMatch 把包名中的连字符和下划线转成空格，便于统一匹配。
func normalizeForMatch(text string) string {
	text = strings.ToLower(text)
	return strings.NewReplacer("-", " ", "_", " ").Replace(text)
}

// needReboot 检查升级输出中是否出现需要重启的软件包关键字。
func needReboot(output string) bool {
	normalized := normalizeForMatch(output)
	for _, keyword := range rebootKeywords {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return false
}

// rebootFlagExists 判断 Debian / Ubuntu 的重启标志文件是否存在。
func rebootFlagExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shouldReboot 合并两种判断条件，任一成立即需要重启。
func shouldReboot(output string, flagPath string) bool {
	return needReboot(output) || rebootFlagExists(flagPath)
}

// runUpgrade 执行完整的升级流程，输出内容通过 write 回调写出。
func runUpgrade(runner commandRunner, paths commandPaths, write func(string), check func(string, string) bool, flagPath string) error {
	out, err := runner.Run(paths.apt, "update", "-y")
	write(out)
	if err != nil {
		return fmt.Errorf("apt update 执行失败：%w", err)
	}

	out, err = runner.Run(paths.apt, "full-upgrade", "-y")
	write(out)
	if err != nil {
		return fmt.Errorf("apt full-upgrade 执行失败：%w", err)
	}

	if check(out, flagPath) {
		write("检测到需要重启的更新，正在执行 reboot\n")
		if _, err := runner.Run(paths.reboot); err != nil {
			return fmt.Errorf("reboot 执行失败：%w", err)
		}
		write("reboot 指令已发送\n")
		return nil
	}

	write("本次升级不需要重启\n")
	return nil
}

// spinner 只在控制终端绘制转圈动画，不写入日志或标准错误。
type spinner struct {
	writer io.Writer
	closer io.Closer
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	mu     sync.Mutex
}

// spinnerFrame 返回指定下标的动画帧。
func spinnerFrame(index int) string {
	return spinnerFrames[index%len(spinnerFrames)]
}

// startSpinner 启动动画；writer 为 nil 时不显示动画。
func startSpinner(w io.Writer) *spinner {
	s := &spinner{writer: w}
	if w == nil {
		return s
	}

	s.done = make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		index := 0
		for {
			select {
			case <-s.done:
				s.Clear()
				return
			default:
				s.mu.Lock()
				fmt.Fprintf(s.writer, "\r%s 正在执行 apt 升级...", spinnerFrame(index))
				s.mu.Unlock()
				index++
				time.Sleep(spinnerInterval)
			}
		}
	}()
	return s
}

// newTTYSpinner 打开当前终端并显示动画；无终端时静默跳过。
func newTTYSpinner() *spinner {
	file, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return startSpinner(nil)
	}
	s := startSpinner(file)
	s.closer = file
	return s
}

// Clear 清除当前动画行，避免和命令输出重叠。
func (s *spinner) Clear() {
	if s == nil || s.writer == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(s.writer, "\r\033[K")
}

// Stop 停止动画并关闭终端文件。
func (s *spinner) Stop() {
	if s == nil || s.done == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
		if s.closer != nil {
			s.closer.Close()
		}
	})
}

// run 执行主流程并返回退出码。
func run() int {
	if !isRoot(os.Geteuid()) {
		fmt.Println("错误：请以 root 身份或使用 sudo 运行本工具")
		return 1
	}

	aptPath, err := resolveCommand("/usr/bin/apt", "/usr/sbin/apt")
	if err != nil {
		fmt.Println("错误：无法找到 apt 命令：", err)
		return 1
	}
	rebootPath, err := resolveCommand("/usr/sbin/reboot", "/sbin/reboot", "/usr/bin/reboot", "/bin/reboot")
	if err != nil {
		fmt.Println("错误：无法找到 reboot 命令：", err)
		return 1
	}

	path, err := logFilePath(os.Executable)
	if err != nil {
		fmt.Println("错误：无法定位日志文件：", err)
		return 1
	}

	logFile, err := prepareLogFile(path)
	if err != nil {
		fmt.Println("错误：无法创建日志文件：", err)
		return 1
	}
	defer logFile.Close()

	spin := newTTYSpinner()
	defer spin.Stop()

	logger := &multiLogger{writers: []io.Writer{os.Stdout, logFile}, now: time.Now}
	write := func(text string) {
		spin.Clear()
		if err := logger.Write(text); err != nil {
			fmt.Fprintln(os.Stderr, "写入日志失败：", err)
		}
	}

	paths := commandPaths{apt: aptPath, reboot: rebootPath}
	if err := runUpgrade(realRunner{}, paths, write, shouldReboot, rebootFlagPath); err != nil {
		write("错误：" + err.Error() + "\n")
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}
