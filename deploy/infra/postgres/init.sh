#!/bin/bash
set -e
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    CREATE USER grafana WITH PASSWORD '${GRAFANA_DB_PASSWORD}';
    CREATE DATABASE grafana OWNER grafana;
EOSQL
