#!/bin/sh
set -eu

for migration in /migrations/*.up.sql; do
    echo "正在执行云令数据库迁移：${migration}"
    psql --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --file "$migration"
done
