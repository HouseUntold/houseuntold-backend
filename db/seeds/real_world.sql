BEGIN;

-- Admin-only seed. Other staff accounts should be created through the admin flow or a separate secure process.
INSERT INTO staff_accounts (id, username, password_hash, role, is_active) VALUES
('00000000-0000-4000-8000-000000000001', 'admin', '$argon2id$v=19$m=65536,t=3,p=2$K8ULF6r3Dt04yCOmxUS6cQ$yK9KXaOm3KdHkfuiJD++EwVPfQW3dSofk7LYeS6j5+s', 'ADMIN', true)
ON CONFLICT (username) DO UPDATE
SET password_hash = EXCLUDED.password_hash,
    role = EXCLUDED.role,
    is_active = EXCLUDED.is_active;

COMMIT;
