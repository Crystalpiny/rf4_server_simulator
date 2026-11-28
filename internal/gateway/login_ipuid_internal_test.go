package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rf4server/internal/config"
)

// syncBuffer 是并发安全的日志缓冲：分配日志可能来自多个 goroutine。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs 返回把全部日志收进缓冲区的 logger，断言失败时能带上实际日志。
func captureLogs() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// newIPUIDTestServer 构造只带 IP→userid 映射表的 LoginServer，
// 不装配 TLS 与存储，让测试聚焦在分配与落盘。
func newIPUIDTestServer(base int64, path string, log *slog.Logger) *LoginServer {
	cfg := config.Gateway{UseridStart: base, UseridMapFile: path}
	return &LoginServer{cfg: cfg, log: log, ipUID: newIPUIDTable(cfg)}
}

// waitForLog 在超时上限内等日志出现，而不是假定"应该已经写完"。
func waitForLog(t *testing.T, logs *syncBuffer, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		text := logs.String()
		if strings.Contains(text, want) {
			return text
		}
		if time.Now().After(deadline) {
			t.Fatalf("日志缺少 %q：期望出现该关键字，实际日志=%s", want, text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIPUIDStableAcrossRestart 钉住「重启后同一 IP 仍是同一个 uid」。
// 映射表写失败曾完全静默，重启时同一 IP 会被当成新玩家重新发号。
func TestIPUIDStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultIPUIDFile)
	logger, logs := captureLogs()

	first := newIPUIDTestServer(1000001, path, logger)
	if got, want := first.useridForRemote("127.0.0.1:50000"), int64(1000001); got != want {
		t.Fatalf("本机应保持固定 base：期望 %d，实际 %d", want, got)
	}
	uid := first.useridForRemote("192.168.1.20:50000")
	if want := int64(1000002); uid != want {
		t.Fatalf("首个局域网 IP 应拿到 base+1：期望 %d，实际 %d", want, uid)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("分配后映射表必须落盘：期望 %s 存在，实际报错 %v", path, err)
	}
	// 原子写不应留下临时文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取工作目录失败: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("原子写不应残留临时文件：实际发现 %s", e.Name())
		}
	}

	// 重启：重新构造 LoginServer（等价于进程重启后重新读表）。
	second := newIPUIDTestServer(1000001, path, logger)
	if got := second.useridForRemote("192.168.1.20:51234"); got != uid {
		t.Fatalf("重启后同一 IP 必须沿用旧 uid：期望 %d，实际 %d，日志=%s", uid, got, logs.String())
	}
	if got, want := second.useridForRemote("192.168.1.21:50000"), int64(1000003); got != want {
		t.Fatalf("重启后新 IP 应接着表内最大值发号：期望 %d，实际 %d", want, got)
	}
}

// TestIPUIDSeedMergesWithExistingFile 校验读表后既沿用旧号，又从最大值继续发号。
func TestIPUIDSeedMergesWithExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultIPUIDFile)
	seed := map[string]int64{"10.1.0.5": 1000007, "10.1.0.6": 1000002}
	b, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("构造种子表失败: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("写入种子表失败: %v", err)
	}
	logger, _ := captureLogs()
	s := newIPUIDTestServer(1000001, path, logger)
	if got, want := s.useridForRemote("10.1.0.5:5000"), int64(1000007); got != want {
		t.Fatalf("已有 IP 应沿用表内 uid：期望 %d，实际 %d", want, got)
	}
	if got, want := s.useridForRemote("10.1.0.9:5000"), int64(1000008); got != want {
		t.Fatalf("新 IP 应取表内最大值 +1：期望 %d，实际 %d", want, got)
	}
}

// TestIPUIDWriteFailureLogged 覆盖写盘失败：必须记录 Error，且本次分配照常返回、不 panic。
func TestIPUIDWriteFailureLogged(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备不可写路径失败: %v", err)
	}
	// 父路径是普通文件，同目录临时文件创建必然失败。
	path := filepath.Join(blocker, defaultIPUIDFile)
	logger, logs := captureLogs()
	s := newIPUIDTestServer(1000001, path, logger)

	uid := s.useridForRemote("10.2.0.5:5000")
	if want := int64(1000002); uid != want {
		t.Fatalf("写盘失败不得影响本次分配：期望 %d，实际 %d", want, uid)
	}
	text := waitForLog(t, logs, "写入 LAN userid 映射表失败")
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, path) {
		t.Fatalf("写盘失败必须记 Error 且带路径 %q：实际日志=%s", path, text)
	}
	if again := s.useridForRemote("10.2.0.5:5001"); again != uid {
		t.Fatalf("写盘失败后同进程内重登仍应稳定：期望 %d，实际 %d", uid, again)
	}
}

// TestIPUIDCorruptFileLogged 覆盖截断 JSON：能识别并记 Error，不 panic，仍能发号。
func TestIPUIDCorruptFileLogged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultIPUIDFile)
	// 半截 JSON 正是原地覆盖期间崩溃会留下的残留。
	if err := os.WriteFile(path, []byte(`{"192.168.1.20": 100000`), 0o644); err != nil {
		t.Fatalf("写入损坏表失败: %v", err)
	}
	logger, logs := captureLogs()
	s := newIPUIDTestServer(1000001, path, logger)

	uid := s.useridForRemote("192.168.1.30:5000")
	if want := int64(1000002); uid != want {
		t.Fatalf("损坏表应按空表重新发号：期望 %d，实际 %d", want, uid)
	}
	text := waitForLog(t, logs, "LAN userid 映射表损坏")
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, path) {
		t.Fatalf("表损坏必须记 Error 且带路径 %q：实际日志=%s", path, text)
	}

	// 重新发号后写回的表必须是合法 JSON，重启不再判坏。
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("重建后的映射表读取失败: %v", err)
	}
	var m map[string]int64
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("重建后的映射表应是合法 JSON：实际内容=%q，解析报错=%v", string(b), err)
	}
	if m["192.168.1.30"] != uid {
		t.Fatalf("重建后的映射表应含本次分配：期望 %s=%d，实际=%v", "192.168.1.30", uid, m)
	}
}

// TestIPUIDConcurrentAllocation 用 20 个不同 IP 并发分配，要求 uid 互不相同且全部落盘。
// -race 下同时校验分配路径没有数据竞争。
func TestIPUIDConcurrentAllocation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultIPUIDFile)
	logger, logs := captureLogs()
	s := newIPUIDTestServer(1000001, path, logger)

	const n = 20
	uids := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uids[i] = s.useridForRemote(fmt.Sprintf("10.3.0.%d:5000", i+1))
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]string, n)
	for i, uid := range uids {
		host := fmt.Sprintf("10.3.0.%d", i+1)
		if uid <= 1000001 {
			t.Fatalf("%s 应拿到 base 之后的 uid：期望 > 1000001，实际 %d", host, uid)
		}
		if prev, dup := seen[uid]; dup {
			t.Fatalf("并发分配出现重复 uid：%s 与 %s 都拿到 %d", prev, host, uid)
		}
		seen[uid] = host
	}

	// 落盘内容必须覆盖全部并发分配，重启后才能全部沿用。
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("并发分配后映射表读取失败: %v", err)
	}
	var m map[string]int64
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("并发分配后映射表应是合法 JSON：解析报错=%v，内容=%q", err, string(b))
	}
	if len(m) != n {
		t.Fatalf("映射表应包含全部 %d 个 IP：实际 %d 个，日志=%s", n, len(m), logs.String())
	}
	fresh := newIPUIDTestServer(1000001, path, logger)
	for i, uid := range uids {
		host := fmt.Sprintf("10.3.0.%d", i+1)
		if got := fresh.useridForRemote(host + ":6000"); got != uid {
			t.Fatalf("重启后 %s 应沿用旧 uid：期望 %d，实际 %d", host, uid, got)
		}
	}
}

// TestNewIPUIDTablePath 校验路径来源：未配置用默认文件名，配置了就用配置值。
func TestNewIPUIDTablePath(t *testing.T) {
	if got := newIPUIDTable(config.Gateway{}).path; got != defaultIPUIDFile {
		t.Fatalf("未配置时应使用默认文件名：期望 %q，实际 %q", defaultIPUIDFile, got)
	}
	custom := filepath.Join("data", "ip_uids.json")
	if got := newIPUIDTable(config.Gateway{UseridMapFile: custom}).path; got != custom {
		t.Fatalf("配置了路径应原样使用：期望 %q，实际 %q", custom, got)
	}
}

// TestWriteFileAtomicOverwrites 校验原子写：覆盖旧内容、继承旧内容的可读性、不留临时文件。
func TestWriteFileAtomicOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultIPUIDFile)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("准备旧文件失败: %v", err)
	}
	if err := writeFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("原子写覆盖已存在文件必须成功：实际报错 %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取覆盖后的文件失败: %v", err)
	}
	if string(b) != "new" {
		t.Fatalf("覆盖后内容应为 new：期望 %q，实际 %q", "new", string(b))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取工作目录失败: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("原子写成功后不应残留临时文件：实际发现 %s", e.Name())
		}
	}
	if err := writeFileAtomic(filepath.Join(path, "x"), []byte("new"), 0o644); err == nil {
		t.Fatalf("目标目录不可写时必须返回错误：期望 error，实际 nil")
	}
}
