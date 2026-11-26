-- name: CreateAPIAuditLog :exec
INSERT INTO api_audit_logs (
  request_id,
  method,
  path,
  route,
  status_code,
  duration_ms,
  actor_id,
  actor_role,
  ip,
  user_agent
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListAPIAuditLogs :many
SELECT id, request_id, method, path, route, status_code, duration_ms, actor_id, actor_role, ip, user_agent, created_at
FROM api_audit_logs
WHERE (sqlc.narg(actor_id)::uuid IS NULL OR actor_id = sqlc.narg(actor_id))
  AND (sqlc.narg(path)::text IS NULL OR path LIKE '%' || sqlc.narg(path) || '%')
ORDER BY created_at DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);
