package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/venu-live/venu-api/internal/db"
	"github.com/venu-live/venu-api/internal/middleware"
)

type AuditSink struct {
	repo *TicketingRepository
}

func NewAuditSink(repo *TicketingRepository) *AuditSink {
	return &AuditSink{repo: repo}
}

func (s *AuditSink) WriteAudit(ctx context.Context, record middleware.AuditRecord) error {
	return s.repo.Read(ctx, func(tctx context.Context, q db.Querier) error {
		params := db.CreateAPIAuditLogParams{
			RequestID:  record.RequestID,
			Method:     record.Method,
			Path:       record.Path,
			Route:      record.Route,
			StatusCode: int32(record.StatusCode),
			DurationMs: int32(record.DurationMS),
			Ip:         record.IP,
			UserAgent:  record.UserAgent,
		}
		if record.ActorID != uuid.Nil {
			params.ActorID = pgtype.UUID{Bytes: record.ActorID, Valid: true}
		}
		if record.ActorRole != "" {
			params.ActorRole = pgtype.Text{String: record.ActorRole, Valid: true}
		}
		return q.CreateAPIAuditLog(tctx, params)
	})
}

func (r *TicketingRepository) ListAPIAuditLogs(ctx context.Context, params db.ListAPIAuditLogsParams) ([]db.ApiAuditLog, error) {
	var rows []db.ApiAuditLog
	err := r.Read(ctx, func(tctx context.Context, q db.Querier) (err error) {
		rows, err = q.ListAPIAuditLogs(tctx, params)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repo_list_api_audit_logs: %w", err)
	}
	return rows, nil
}
