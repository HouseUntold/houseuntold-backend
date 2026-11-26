-- name: CountActiveAdmins :one
SELECT COUNT(*)
FROM staff_accounts
WHERE role = 'ADMIN' AND is_active = true;

-- name: ListStaff :many
SELECT id, username, role, is_active, token_version, created_at
FROM staff_accounts
ORDER BY created_at DESC;

-- name: GetStaffByUsername :one
SELECT id, username, password_hash, role, is_active, token_version, created_at
FROM staff_accounts
WHERE username = $1;

-- name: GetStaffByID :one
SELECT id, username, password_hash, role, is_active, token_version, created_at
FROM staff_accounts
WHERE id = $1;

-- name: CreateStaff :one
INSERT INTO staff_accounts (username, password_hash, role, is_active)
VALUES ($1, $2, $3, true)
RETURNING id, username, password_hash, role, is_active, token_version, created_at;

-- name: UpdateStaffPassword :one
UPDATE staff_accounts
SET password_hash = $2, token_version = token_version + 1
WHERE id = $1 AND is_active = true
RETURNING id, username, password_hash, role, is_active, token_version, created_at;

-- name: DeactivateStaff :one
UPDATE staff_accounts
SET is_active = false, token_version = token_version + 1
WHERE id = $1 AND is_active = true
RETURNING id, username, password_hash, role, is_active, token_version, created_at;

-- name: IncrementStaffTokenVersion :one
UPDATE staff_accounts
SET token_version = token_version + 1
WHERE id = $1 AND is_active = true
RETURNING id, username, password_hash, role, is_active, token_version, created_at;

-- name: RotateStaffTokenVersion :one
UPDATE staff_accounts
SET token_version = token_version + 1
WHERE id = $1 AND token_version = $2 AND is_active = true
RETURNING id, username, password_hash, role, is_active, token_version, created_at;
