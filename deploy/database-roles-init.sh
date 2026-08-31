#!/bin/sh
set -eu
umask 077

backup_password_file=/run/secrets/backup-postgres-password
verify_password_file=/run/secrets/verify-postgres-password

for secret_file in "$backup_password_file" "$verify_password_file"; do
    if [ ! -s "$secret_file" ]; then
        echo "数据库专用账号密钥不存在或为空：${secret_file}" >&2
        exit 1
    fi
done

YUNLING_BACKUP_POSTGRES_PASSWORD="$(tr -d '\r\n' < "$backup_password_file")"
YUNLING_VERIFY_POSTGRES_PASSWORD="$(tr -d '\r\n' < "$verify_password_file")"
export YUNLING_BACKUP_POSTGRES_PASSWORD YUNLING_VERIFY_POSTGRES_PASSWORD

psql --set ON_ERROR_STOP=1 \
    --set app_db="$YUNLING_POSTGRES_DB" \
    --set app_owner="$YUNLING_POSTGRES_USER" <<'SQL'
\getenv backup_password YUNLING_BACKUP_POSTGRES_PASSWORD
\getenv verify_password YUNLING_VERIFY_POSTGRES_PASSWORD

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yunling_backup') THEN
        CREATE ROLE yunling_backup LOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yunling_verifier') THEN
        CREATE ROLE yunling_verifier LOGIN CREATEDB;
    END IF;
END
$$;

ALTER ROLE yunling_backup NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
ALTER ROLE yunling_verifier NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION;
SELECT format('ALTER ROLE yunling_backup PASSWORD %L', :'backup_password') \gexec
SELECT format('ALTER ROLE yunling_verifier PASSWORD %L', :'verify_password') \gexec

GRANT CONNECT ON DATABASE :"app_db" TO yunling_backup;
GRANT USAGE ON SCHEMA public TO yunling_backup;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO yunling_backup;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO yunling_backup;
ALTER DEFAULT PRIVILEGES FOR ROLE :"app_owner" IN SCHEMA public GRANT SELECT ON TABLES TO yunling_backup;
ALTER DEFAULT PRIVILEGES FOR ROLE :"app_owner" IN SCHEMA public GRANT SELECT ON SEQUENCES TO yunling_backup;
SQL

unset YUNLING_BACKUP_POSTGRES_PASSWORD YUNLING_VERIFY_POSTGRES_PASSWORD
echo "数据库备份与隔离恢复账号已就绪"
