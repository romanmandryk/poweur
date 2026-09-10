\getenv grafana_password GRAFANA_DB_PASSWORD
CREATE USER grafana WITH PASSWORD :'grafana_password';
CREATE DATABASE grafana OWNER grafana;
