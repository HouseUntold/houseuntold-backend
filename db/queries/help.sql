-- name: CreateHelpMessage :one
INSERT INTO help_messages (name, phone, message, event_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListHelpMessages :many
SELECT id, name, phone, message, event_id, is_resolved, resolved_by, resolved_at, created_at
FROM help_messages
WHERE (sqlc.narg(is_resolved)::boolean IS NULL OR is_resolved = sqlc.narg(is_resolved))
ORDER BY created_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: ResolveHelpMessage :one
UPDATE help_messages
SET is_resolved = true, resolved_by = $2, resolved_at = NOW()
WHERE id = $1 AND is_resolved = false
RETURNING *;
