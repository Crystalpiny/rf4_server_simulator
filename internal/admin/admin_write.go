package admin

// 管理写操作的「审计 + 指令入队」提交入口。
//
// 变更类接口通常要同时留下审计行并投递一条 admin_queue 指令。两者分别执行时，
// 任一写入失败都无法被调用方感知，接口照样回 200：管理员看到操作成功，而指令
// 从未抵达 gameserver、敏感操作也没有审计痕迹。这里统一放进同一事务提交，
// 失败即整笔回滚并回 5xx。

import (
	"context"
	"net/http"
)

// adminAudit 一条待写审计行。
type adminAudit struct {
	action string
	target string
	detail string
}

// adminWrite 一次管理写操作要提交的副作用：若干审计行与若干队列指令。
type adminWrite struct {
	admin  string
	audits []adminAudit
	queue  []string
}

// audit 追加一条审计行。
func (w *adminWrite) audit(action, target, detail string) {
	w.audits = append(w.audits, adminAudit{action: action, target: target, detail: detail})
}

// push 追加一条 admin_queue 指令，按追加顺序入队。
func (w *adminWrite) push(payload string) {
	w.queue = append(w.queue, payload)
}

// commit 在一个事务里写入全部审计行与队列指令，任一失败即整笔回滚：同连接、
// 同提交，避免出现「审计记了、指令没入队」的分裂状态。
func (s *Server) commit(ctx context.Context, w adminWrite) error {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range w.audits {
		if err := s.db.AuditLogWriteTx(ctx, tx, w.admin, a.action, a.target, a.detail); err != nil {
			return err
		}
	}
	for _, payload := range w.queue {
		if err := s.db.QueuePushTx(ctx, tx, payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// commitAdmin 提交管理写操作：失败时记录操作与目标并回 5xx，
// 返回 false 表示已经写出错误响应，调用方直接 return。
func (s *Server) commitAdmin(ctx context.Context, w http.ResponseWriter, aw adminWrite) bool {
	if err := s.commit(ctx, aw); err != nil {
		attrs := []any{"admin", aw.admin, "audits", len(aw.audits), "queue", len(aw.queue), "err", err}
		if len(aw.audits) > 0 {
			attrs = append(attrs, "action", aw.audits[0].action, "target", aw.audits[0].target)
		}
		if len(aw.queue) > 0 {
			attrs = append(attrs, "payload", trimLog(aw.queue[0], 160))
		}
		s.log.l.Error("管理操作未生效：审计或指令写入失败", attrs...)
		httpError(w, 500, "操作未生效：审计或指令写入失败")
		return false
	}
	return true
}

// trimLog 截断过长字段，避免单条日志被大 payload 撑爆。
func trimLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
