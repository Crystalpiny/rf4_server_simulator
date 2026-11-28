package config

import (
	"os"
	"path/filepath"
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

// 发布包开箱即用：管理后台只监听本机回环，凭据仍为可登录的 admin/admin123。
func TestAdminDefaultsStayOpenOutOfBox(t *testing.T) {
	want := Admin{
		HTTPAddr:  "127.0.0.1:8080",
		JWTSecret: DefaultAdminJWTSecret,
		ObsAddr:   ":9503",
		Username:  DefaultAdminUsername,
		Password:  DefaultAdminPassword,
	}
	if got := defaults().Admin; got != want {
		t.Fatalf("管理后台默认值应为 %+v，实得 %+v", want, got)
	}
	user, pass := defaults().Admin.EffectiveCredentials()
	if user != "admin" || pass != "admin123" {
		t.Fatalf("默认有效凭据应为 admin/admin123，实得 %s/%s", user, pass)
	}
}

// 显式配置必须覆盖默认值，包括把监听地址放回所有网卡。
func TestAdminConfigOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "admin:" + "\n" +
		"  http_addr: \":8080\"" + "\n" +
		"  jwt_secret: \"lan-secret\"" + "\n" +
		"  username: \"boss\"" + "\n" +
		"  password: \"strong-pass\"" + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	if cfg.Admin.HTTPAddr != ":8080" {
		t.Fatalf("http_addr 应取配置值 :8080，实得 %q", cfg.Admin.HTTPAddr)
	}
	if cfg.Admin.JWTSecret != "lan-secret" {
		t.Fatalf("jwt_secret 应取配置值 lan-secret，实得 %q", cfg.Admin.JWTSecret)
	}
	if user, pass := cfg.Admin.EffectiveCredentials(); user != "boss" || pass != "strong-pass" {
		t.Fatalf("有效凭据应取配置值 boss/strong-pass，实得 %s/%s", user, pass)
	}
	if !cfg.Admin.ListenAllInterfaces() {
		t.Fatalf("显式配置 %q 时应判定为监听所有网卡，实际没有", cfg.Admin.HTTPAddr)
	}
	if got := cfg.Admin.WeakWarnings(); len(got) != 0 {
		t.Fatalf("自定义凭据与密钥不应产生告警，实得 %v", got)
	}
}

// 弱口令/默认密钥必须被识别成告警，自定义值不得误报。
func TestAdminWeakWarningsReportDefaultCredentials(t *testing.T) {
	weak := Admin{Username: "admin", Password: "admin123", JWTSecret: "change-me"}
	got := weak.WeakWarnings()
	if len(got) != 2 {
		t.Fatalf("默认凭据与默认密钥应各出一条告警，实得 %d 条: %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"admin.password", "admin123", "admin.jwt_secret"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("弱口令告警应包含 %q 以便照着改，实得: %s", want, joined)
		}
	}

	empty := Admin{Username: "admin", Password: "", JWTSecret: ""}
	if got := empty.WeakWarnings(); len(got) != 2 {
		t.Fatalf("空密码与空密钥应各出一条告警，实得 %d 条: %v", len(got), got)
	}

	strong := Admin{Username: "admin", Password: "strong-pass", JWTSecret: "lan-secret"}
	if got := strong.WeakWarnings(); len(got) != 0 {
		t.Fatalf("自定义凭据不应告警，实得 %v", got)
	}
}

// 只有回环地址才算安全默认；通配地址与主机名都视为对外开放。
func TestAdminListenAllInterfaces(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"[::1]:8080", false},
		{":8080", true},
		{"0.0.0.0:8080", true},
		{"192.168.1.10:8080", true},
		{"admin.example.com:8080", true},
		{"", true},
	}
	for _, tc := range cases {
		if got := (Admin{HTTPAddr: tc.addr}).ListenAllInterfaces(); got != tc.want {
			t.Fatalf("http_addr=%q 应判定监听所有网卡=%v，实得 %v", tc.addr, tc.want, got)
		}
	}
}
