package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 假命令执行器：记录调用过的指令，不真实执行系统命令。
type fakeRunner struct {
	calls []string
	out   string
	err   error
}

func (r *fakeRunner) Run(name string, args ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	return r.out, r.err
}

func TestIsRoot(t *testing.T) {
	if !isRoot(0) {
		t.Error("euid 为 0 时应判定为 root")
	}
	if isRoot(1000) {
		t.Error("普通用户不应判定为 root")
	}
}

func TestLogFilePath(t *testing.T) {
	path, err := logFilePath(func() (string, error) {
		return "/opt/tools/apt_upgrade", nil
	})
	if err != nil {
		t.Fatalf("解析日志路径失败：%v", err)
	}
	if want := filepath.Join("/opt/tools", "apt_upgrade.log"); path != want {
		t.Errorf("日志路径错误，期望 %q，实际 %q", want, path)
	}
}

func TestLogFilePathExecutableError(t *testing.T) {
	want := errors.New("找不到可执行文件")
	_, err := logFilePath(func() (string, error) { return "", want })
	if !errors.Is(err, want) {
		t.Errorf("应传递可执行文件解析错误，实际为 %v", err)
	}
}

func TestPrepareLogFileCreatesAndClears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apt_upgrade.log")
	if err := os.WriteFile(path, []byte("旧的日志内容"), 0o600); err != nil {
		t.Fatalf("准备旧日志文件失败：%v", err)
	}

	file, err := prepareLogFile(path)
	if err != nil {
		t.Fatalf("初始化日志文件失败：%v", err)
	}
	defer file.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志文件失败：%v", err)
	}
	if len(content) != 0 {
		t.Errorf("启动时应清空日志文件，实际内容为 %q", string(content))
	}
}

func TestPrepareLogFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apt_upgrade.log")
	file, err := prepareLogFile(path)
	if err != nil {
		t.Fatalf("初始化日志文件失败：%v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("读取日志文件属性失败：%v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("日志文件权限应为 0600，实际为 %04o", got)
	}
}

func TestPrepareLogFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "重要文件")
	if err := os.WriteFile(target, []byte("保留内容"), 0o600); err != nil {
		t.Fatalf("创建目标文件失败：%v", err)
	}
	link := filepath.Join(dir, "apt_upgrade.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("创建符号链接失败：%v", err)
	}

	if _, err := prepareLogFile(link); err == nil {
		t.Error("日志路径为符号链接时应拒绝打开")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取目标文件失败：%v", err)
	}
	if string(content) != "保留内容" {
		t.Errorf("符号链接目标不应被清空，实际内容为 %q", string(content))
	}
}

func TestFormatLogLine(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 30, 5, 0, time.UTC)
	got := formatLogLine(now, "升级完成")
	want := "2026-11-25 10:30:05 UTC | 升级完成"
	if got != want {
		t.Errorf("日志行格式错误，期望 %q，实际 %q", want, got)
	}
}

func TestWriteLog(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 25, 10, 30, 5, 0, time.UTC)
	if err := writeLog(&buf, now, "第一行\n第二行\n"); err != nil {
		t.Fatalf("写入日志失败：%v", err)
	}
	want := "2026-11-25 10:30:05 UTC | 第一行\n2026-11-25 10:30:05 UTC | 第二行\n"
	if buf.String() != want {
		t.Errorf("日志内容错误，期望 %q，实际 %q", want, buf.String())
	}
}

func TestWriteLogRedactsCredentials(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 25, 10, 30, 5, 0, time.UTC)
	text := "获取 https://user:secret@example.com/debian 失败"
	if err := writeLog(&buf, now, text); err != nil {
		t.Fatalf("写入日志失败：%v", err)
	}
	want := "2026-11-25 10:30:05 UTC | 获取 https://***@example.com/debian 失败\n"
	if buf.String() != want {
		t.Errorf("应脱敏 URL 中的凭据，期望 %q，实际 %q", want, buf.String())
	}
}

func TestMultiLoggerWrite(t *testing.T) {
	var stdout, logFile bytes.Buffer
	logger := &multiLogger{
		writers: []io.Writer{&stdout, &logFile},
		now: func() time.Time {
			return time.Date(2026, 11, 25, 10, 30, 5, 0, time.UTC)
		},
	}
	if err := logger.Write("升级完成\n"); err != nil {
		t.Fatalf("写入多输出日志失败：%v", err)
	}
	want := "2026-11-25 10:30:05 UTC | 升级完成\n"
	if stdout.String() != want {
		t.Errorf("标准输出内容错误，期望 %q，实际 %q", want, stdout.String())
	}
	if logFile.String() != want {
		t.Errorf("日志文件内容错误，期望 %q，实际 %q", want, logFile.String())
	}
}

func TestCommandEnv(t *testing.T) {
	t.Setenv("APT_UPGRADE_SECRET_TEST", "不应泄露给子进程")
	env := strings.Join(commandEnv(), "\n")

	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("子进程环境应包含 %q，实际为 %q", want, env)
		}
	}
	if strings.Contains(env, "APT_UPGRADE_SECRET_TEST") {
		t.Errorf("不应继承无关的 root 环境变量，实际为 %q", env)
	}
}

func TestResolveCommand(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "apt")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("创建可执行文件失败：%v", err)
	}
	notExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("内容"), 0o644); err != nil {
		t.Fatalf("创建普通文件失败：%v", err)
	}

	got, err := resolveCommand(filepath.Join(dir, "不存在"), notExecutable, executable)
	if err != nil {
		t.Fatalf("解析命令路径失败：%v", err)
	}
	if got != executable {
		t.Errorf("应返回第一个可执行文件，期望 %q，实际 %q", executable, got)
	}
	if _, err := resolveCommand(filepath.Join(dir, "不存在"), notExecutable); err == nil {
		t.Error("没有可执行候选时应返回错误")
	}
}

func TestNeedReboot(t *testing.T) {
	cases := map[string]bool{
		" upgraded linux kernel package ": true,
		"Installed linux-image-6.1":       true,
		"更新了 SSL 证书":                      true,
		"重启 ssh 服务":                       true,
		"NGINX 已升级":                       true,
		"只更新了 curl 工具":                    false,
	}
	for output, want := range cases {
		if got := needReboot(output); got != want {
			t.Errorf("needReboot(%q) 期望 %v，实际 %v", output, want, got)
		}
	}
}

func TestRebootFlagExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reboot-required")
	if rebootFlagExists(path) {
		t.Error("标志文件不存在时应返回 false")
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("创建标志文件失败：%v", err)
	}
	if !rebootFlagExists(path) {
		t.Error("标志文件存在时应返回 true")
	}
}

func TestShouldReboot(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "不存在")
	existing := filepath.Join(dir, "reboot-required")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatalf("创建标志文件失败：%v", err)
	}

	if !shouldReboot("linux kernel 已升级", missing) {
		t.Error("关键字命中时应重启")
	}
	if !shouldReboot("无关键字输出", existing) {
		t.Error("/var/run/reboot-required 存在时应重启")
	}
	if shouldReboot("无关键字输出", missing) {
		t.Error("两个条件都不满足时不应重启")
	}
}

func TestRunUpgradeSuccessWithReboot(t *testing.T) {
	paths := commandPaths{apt: "/usr/bin/apt", reboot: "/usr/sbin/reboot"}
	runner := &fakeRunner{out: "升级完成\n"}
	var messages []string
	write := func(text string) { messages = append(messages, text) }
	check := func(output, flag string) bool { return true }

	if err := runUpgrade(runner, paths, write, check, "/var/run/reboot-required"); err != nil {
		t.Fatalf("执行升级流程失败：%v", err)
	}
	wantCalls := []string{
		paths.apt + " update -y",
		paths.apt + " full-upgrade -y",
		paths.reboot,
	}
	if strings.Join(runner.calls, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("命令顺序错误，期望 %v，实际 %v", wantCalls, runner.calls)
	}
	if !strings.Contains(strings.Join(messages, ""), "正在执行 reboot") {
		t.Error("应输出重启提示")
	}
}

func TestRunUpgradeWithoutReboot(t *testing.T) {
	paths := commandPaths{apt: "/usr/bin/apt", reboot: "/usr/sbin/reboot"}
	runner := &fakeRunner{out: "没有需要重启的更新\n"}
	var messages []string
	write := func(text string) { messages = append(messages, text) }
	check := func(output, flag string) bool { return false }

	if err := runUpgrade(runner, paths, write, check, "/var/run/reboot-required"); err != nil {
		t.Fatalf("执行升级流程失败：%v", err)
	}
	wantCalls := []string{paths.apt + " update -y", paths.apt + " full-upgrade -y"}
	if strings.Join(runner.calls, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("不应执行 reboot，期望 %v，实际 %v", wantCalls, runner.calls)
	}
}

func TestRunUpgradeStopsOnError(t *testing.T) {
	paths := commandPaths{apt: "/usr/bin/apt", reboot: "/usr/sbin/reboot"}
	runner := &fakeRunner{err: errors.New("apt 执行失败")}
	write := func(string) {}

	if err := runUpgrade(runner, paths, write, shouldReboot, "/var/run/reboot-required"); err == nil {
		t.Error("apt update 失败时应返回错误")
	}
	if len(runner.calls) != 1 {
		t.Errorf("失败后不应继续执行，实际调用 %v", runner.calls)
	}
}

func TestSpinnerFrame(t *testing.T) {
	frames := []string{"⠋", "⠙", "⠹"}
	for i, want := range frames {
		if got := spinnerFrame(i); got != want {
			t.Errorf("第 %d 帧期望 %q，实际 %q", i, want, got)
		}
	}
}

func TestSpinnerWithoutTerminal(t *testing.T) {
	spin := startSpinner(nil)
	spin.Clear()
	spin.Stop()
}
