-- Development-only credentials for the Trickster developer environment.
-- These roles are created once by the TimescaleDB container entrypoint on
-- first initialization of the data volume, connected to the trickster
-- database as the postgres superuser. Run `make developer-delete` to reset.

-- The image's own init script already creates the extension in this database;
-- this keeps the file correct if it is ever run against a plain database.
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- seeder: used by the timescaledb_seed container to drop/create/load the
-- trips hypertable. Needs DDL + DML in the trickster database only. It owns
-- the tables it creates, which covers INSERT/SELECT/TRUNCATE on them.
CREATE ROLE seeder LOGIN PASSWORD 'trickster-dev-seed';
GRANT CONNECT ON DATABASE trickster TO seeder;
GRANT USAGE, CREATE ON SCHEMA public TO seeder;

-- trickster: used by Trickster's upstream connection to the TimescaleDB
-- origin. Read-only.
CREATE ROLE trickster LOGIN PASSWORD 'trickster-dev-upstream';
GRANT CONNECT ON DATABASE trickster TO trickster;
GRANT USAGE ON SCHEMA public TO trickster;

-- grafana_ro: used by the provisioned Grafana PostgreSQL data source.
-- Read-only.
CREATE ROLE grafana_ro LOGIN PASSWORD 'trickster-dev-grafana';
GRANT CONNECT ON DATABASE trickster TO grafana_ro;
GRANT USAGE ON SCHEMA public TO grafana_ro;

-- Every table the seeder creates from now on is readable by the read-only
-- roles, so re-seeding (drop + create) never needs to re-grant.
ALTER DEFAULT PRIVILEGES FOR ROLE seeder IN SCHEMA public
  GRANT SELECT ON TABLES TO trickster, grafana_ro;
