package gateway

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"rf4server/internal/config"
	"rf4server/internal/metrics"
	"rf4server/internal/proto"
	"rf4server/internal/store"
)

// buildAuthPacket 组认证包：01 <重连标记> + u32 长度 + token。
func buildAuthPacket(token string) []byte {
	pkt := append([]byte{proto.AuthMagic, 0x00}, proto.PackU32(uint32(len(token)))...)
	return append(pkt, token...)
}

// authFailureCount 返回 gateway_realtime_auth_failures_total 的当前值；未注册时返回 -1。
func authFailureCount(t *testing.T, obs *metrics.Server) float64 {
	t.Helper()
	families, err := obs.Registry().Gather()
	if err != nil {
		t.Fatalf("读取指标失败: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "gateway_realtime_auth_failures_total" {
			continue
		}
		all := mf.GetMetric()
		if len(all) != 1 {
			t.Fatalf("认证失败计数器有 %d 个样本，期望 1", len(all))
		}
		return all[0].GetCounter().GetValue()
	}
	return -1
}

// TestRealtimeRejectsUnregisteredToken 锁死鉴权边界：格式合法但从未签发的 token 必须被拒绝。
//
// 判别点：认证包里的 token 同时是 RC4 密钥，而 proto.validAuthToken 只校验字符集与竖线数量。
// 只要实现肯从 token 里解析 uid，手写 a|b|1000001|c 就能以任意 uid 接入 realtime，
// 完全跳过 gw_token 的签发校验。
//
// 断言三件事：不发问候就断开、不建立任何会话、认证失败指标 +1。
func TestRealtimeRejectsUnregisteredToken(t *testing.T) {
	obs := metrics.NewServer("", "gateway-auth-test")
	log := slog.New(slog.DiscardHandler)
	cfg := config.Gateway{RealtimeAddr: "127.0.0.1:0", MaxConns: 8}
	// 拒绝发生在隧道握手之前，这里指向一个不可达地址即可：一旦走到隧道就说明已经放行。
	tm := NewTunnelManager("127.0.0.1:1", "auth-test-psk", log, nil, nil)
	rt := NewRealtimeServer(cfg, NewMemStore(1000001), log, tm, obs)
	if err := rt.Listen(); err != nil {
		t.Fatalf("监听 realtime 失败: %v", err)
	}
	defer func() { _ = rt.Close() }()
	go func() { _ = rt.Serve() }()

	forged := "a|b|1000001|c"
	if _, _, ok := proto.TryParseAuthPacket(buildAuthPacket(forged)); !ok {
		t.Fatalf("伪造 token %q 不满足认证包格式，测试前提不成立", forged)
	}

	conn, err := net.DialTimeout("tcp", rt.RealAddr(), 3*time.Second)
	if err != nil {
		t.Fatalf("连接 realtime 失败: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(buildAuthPacket(forged)); err != nil {
		t.Fatalf("发送认证包失败: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, readErr := conn.Read(buf)
	if readErr == nil {
		t.Fatalf("伪造 token 收到 %d 字节应答（%q），期望连接被直接关闭", n, buf[:n])
	}
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatal("伪造 token 的连接既没有放行也没有关闭（读超时），鉴权分支没有拒绝它")
	}

	rt.sids.Range(func(_, _ any) bool {
		t.Error("伪造 token 建立了 realtime 会话：鉴权被绕过，客户端可以冒用任意 uid")
		return false
	})

	if got := authFailureCount(t, obs); got != 1 {
		t.Fatalf("认证失败计数 %v，期望 1：拒绝必须计入 gateway_realtime_auth_failures_total", got)
	}
}

// TestSQLiteStoreLookupByLogintokenAndPrefix 覆盖 realtime 鉴权依赖的正规路径：
// 登录签发的 logintoken 能直接命中，客户端回传的 token 前缀能映射回完整 token；
// 未签发的 token 必须一条都查不到。
func TestSQLiteStoreLookupByLogintokenAndPrefix(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gw-token.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("迁移测试库失败: %v", err)
	}
	s := NewSQLiteStore(db, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	const uid = int64(1000001)
	info := TokenInfo{Userid: uid, IssuedAt: time.Now().Unix()}
	token := NewToken(uid)
	logintoken := NewLogintoken(uid)
	for _, tok := range []string{token, logintoken} {
		if err := s.SaveToken(ctx, tok, info, time.Hour); err != nil {
			t.Fatalf("登记签发凭据失败: %v", err)
		}
	}

	hits := []struct {
		name string
		tok  string
	}{
		{"完整 token", token},
		{"logintoken", logintoken},
		{"token 前缀", token[:64]},
	}
	for _, hit := range hits {
		got, found, err := s.LookupToken(ctx, hit.tok)
		if err != nil {
			t.Fatalf("%s 查询出错: %v", hit.name, err)
		}
		if !found {
			t.Fatalf("%s 查不到签发记录：realtime 会把它当成未登记凭据拒绝", hit.name)
		}
		if got.Userid != uid {
			t.Fatalf("%s 解析出的 uid=%d，期望 %d", hit.name, got.Userid, uid)
		}
	}

	if _, found, err := s.LookupToken(ctx, "a|b|1000001|c"); err != nil || found {
		t.Fatalf("未签发的 token 查到了记录（found=%v err=%v）：realtime 会放行伪造凭据", found, err)
	}
}
