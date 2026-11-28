package config

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestStaticDatabasePathDefaultAndEnvOverride(t *testing.T) {
	if got := defaults().SQLite.StaticPath; got != "server_data/static.sqlite3" {
		t.Fatalf("默认静态库路径=%q", got)
	}
	if got := defaults().Launcher.GameRegion; got != "CN" {
		t.Fatalf("默认游戏区域=%q", got)
	}
	t.Setenv("RF4_SQLITE_STATIC_PATH", "custom/static.sqlite3")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SQLite.StaticPath; got != "custom/static.sqlite3" {
		t.Fatalf("环境变量静态库路径=%q", got)
	}
}

func TestAdminPasswordEnvOverride(t *testing.T) {
	t.Setenv("RF4_ADMIN_PASSWORD", "compose-test-password")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Password != "compose-test-password" {
		t.Fatal("RF4_ADMIN_PASSWORD did not override the configured password")
	}
}

func TestLoginCompatEnvOverride(t *testing.T) {
	t.Setenv("RF4_GATEWAY_LOGIN_COMPAT_ADDR", ":7515")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.LoginCompatAddr != ":7515" {
		t.Fatalf("RF4_GATEWAY_LOGIN_COMPAT_ADDR=%q", cfg.Gateway.LoginCompatAddr)
	}
}

// TestValidateRejectsInvalidMaxConns 挡住两种会把接入面打死的取值。
//
// max_conns: 0 让 sem 变成无缓冲 channel，第二条连接起 Accept 循环永久阻塞；
// 负数让 make(chan, n) 直接 panic。两者都必须在加载配置时就报错，而不是等到
// 第一条连接卡在握手上。
func TestValidateRejectsInvalidMaxConns(t *testing.T) {
	for _, n := range []int{0, -5} {
		cfg := defaults()
		cfg.Gateway.MaxConns = n
		err := cfg.validate()
		if !errors.Is(err, ErrMaxConns) {
			t.Fatalf("max_conns=%d 应被拒绝，实际 err=%v", n, err)
		}
		if !strings.Contains(err.Error(), "gateway.max_conns") {
			t.Fatalf("错误信息未指出配置项名：%v", err)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(n)) {
			t.Fatalf("错误信息未带上当前值 %d：%v", n, err)
		}
	}
}

// TestValidateMaxConnsBounds 钉住合法区间与边界，同时守住默认值不变。
func TestValidateMaxConnsBounds(t *testing.T) {
	if DefaultMaxConns != 512 {
		t.Fatalf("默认 max_conns 变了：%d", DefaultMaxConns)
	}
	if got := defaults().Gateway.MaxConns; got != DefaultMaxConns {
		t.Fatalf("默认配置 max_conns=%d", got)
	}

	for _, tc := range []struct {
		name string
		n    int
		ok   bool
	}{
		{"下限", 1, true},
		{"默认值", DefaultMaxConns, true},
		{"上限", MaxAllowedConns, true},
		{"上限加一", MaxAllowedConns + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults()
			cfg.Gateway.MaxConns = tc.n
			err := cfg.validate()
			if tc.ok && err != nil {
				t.Fatalf("max_conns=%d 应通过，实际 err=%v", tc.n, err)
			}
			if !tc.ok && !errors.Is(err, ErrMaxConns) {
				t.Fatalf("max_conns=%d 应被拒绝，实际 err=%v", tc.n, err)
			}
		})
	}
}

// TestLoadRejectsInvalidMaxConns 走完整加载链路（默认值 + 环境变量 + 校验）。
func TestLoadRejectsInvalidMaxConns(t *testing.T) {
	t.Setenv("RF4_GATEWAY_MAX_CONNS", "0")
	if _, err := Load(""); !errors.Is(err, ErrMaxConns) {
		t.Fatalf("RF4_GATEWAY_MAX_CONNS=0 应在加载时报错，实际 err=%v", err)
	}

	t.Setenv("RF4_GATEWAY_MAX_CONNS", "64")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.MaxConns != 64 {
		t.Fatalf("RF4_GATEWAY_MAX_CONNS 覆盖失败：%d", cfg.Gateway.MaxConns)
	}
}
