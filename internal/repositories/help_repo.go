package repositories

import (
	"context"
	"fmt"

	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) CreateHelpMessage(ctx context.Context, arg db.CreateHelpMessageParams) (db.HelpMessage, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	msg, err := r.queries.CreateHelpMessage(tctx, arg)
	if err != nil {
		return db.HelpMessage{}, fmt.Errorf("repo_create_help_message: %w", err)
	}
	return msg, nil
}

func (r *TicketingRepository) ListHelpMessages(ctx context.Context, arg db.ListHelpMessagesParams) ([]db.HelpMessage, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	msgs, err := r.queries.ListHelpMessages(tctx, arg)
	if err != nil {
		return nil, fmt.Errorf("repo_list_help_messages: %w", err)
	}
	return msgs, nil
}

func (r *TicketingRepository) ResolveHelpMessage(ctx context.Context, arg db.ResolveHelpMessageParams) (db.HelpMessage, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	msg, err := r.queries.ResolveHelpMessage(tctx, arg)
	if err != nil {
		return db.HelpMessage{}, fmt.Errorf("repo_resolve_help_message: %w", err)
	}
	return msg, nil
}
