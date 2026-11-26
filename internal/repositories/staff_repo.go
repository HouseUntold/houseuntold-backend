package repositories

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/venu-live/venu-api/internal/db"
)

func (r *TicketingRepository) GetStaffByUsername(ctx context.Context, username string) (db.StaffAccount, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	staff, err := r.queries.GetStaffByUsername(tctx, username)
	if err != nil {
		return db.StaffAccount{}, fmt.Errorf("repo_get_staff_by_username: %w", err)
	}
	return db.StaffAccountFromGetStaffByUsername(staff), nil
}

func (r *TicketingRepository) GetStaffByID(ctx context.Context, id uuid.UUID) (db.StaffAccount, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	staff, err := r.queries.GetStaffByID(tctx, id)
	if err != nil {
		return db.StaffAccount{}, fmt.Errorf("repo_get_staff_by_id: %w", err)
	}
	return db.StaffAccountFromGetStaffByID(staff), nil
}

func (r *TicketingRepository) CountActiveAdmins(ctx context.Context) (int64, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	count, err := r.queries.CountActiveAdmins(tctx)
	if err != nil {
		return 0, fmt.Errorf("repo_count_active_admins: %w", err)
	}
	return count, nil
}

func (r *TicketingRepository) CreateStaff(ctx context.Context, arg db.CreateStaffParams) (db.StaffAccount, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	staff, err := r.queries.CreateStaff(tctx, arg)
	if err != nil {
		return db.StaffAccount{}, fmt.Errorf("repo_create_staff: %w", err)
	}
	return db.StaffAccountFromCreateStaff(staff), nil
}

func (r *TicketingRepository) UpdateStaffPassword(ctx context.Context, arg db.UpdateStaffPasswordParams) (db.UpdateStaffPasswordRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.UpdateStaffPassword(tctx, arg)
	if err != nil {
		return db.UpdateStaffPasswordRow{}, fmt.Errorf("repo_update_staff_password: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) DeactivateStaff(ctx context.Context, id uuid.UUID) (db.DeactivateStaffRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.DeactivateStaff(tctx, id)
	if err != nil {
		return db.DeactivateStaffRow{}, fmt.Errorf("repo_deactivate_staff: %w", err)
	}
	return row, nil
}

func (r *TicketingRepository) IncrementStaffTokenVersion(ctx context.Context, id uuid.UUID) error {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	if _, err := r.queries.IncrementStaffTokenVersion(tctx, id); err != nil {
		return fmt.Errorf("repo_increment_staff_token_version: %w", err)
	}
	return nil
}

func (r *TicketingRepository) RotateStaffTokenVersion(ctx context.Context, arg db.RotateStaffTokenVersionParams) (db.StaffAccount, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	row, err := r.queries.RotateStaffTokenVersion(tctx, arg)
	if err != nil {
		return db.StaffAccount{}, fmt.Errorf("repo_rotate_staff_token_version: %w", err)
	}
	return db.StaffAccount{
		ID:           row.ID,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		Role:         row.Role,
		IsActive:     row.IsActive,
		TokenVersion: row.TokenVersion,
		CreatedAt:    row.CreatedAt,
	}, nil
}

func (r *TicketingRepository) ListStaff(ctx context.Context) ([]db.ListStaffRow, error) {
	tctx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.queries.ListStaff(tctx)
	if err != nil {
		return nil, fmt.Errorf("repo_list_staff: %w", err)
	}
	return rows, nil
}
