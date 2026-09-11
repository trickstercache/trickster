-- Development-only credentials for the Trickster developer environment.
-- These users are created once by the MySQL container entrypoint on first
-- initialization of the data volume. Run `make developer-delete` to reset.

-- seeder: used by the mysql_seed container to create/truncate/load the
-- trips table. Needs DDL + DML on the trickster database only.
CREATE USER IF NOT EXISTS 'seeder'@'%' IDENTIFIED BY 'trickster-dev-seed';
GRANT CREATE, DROP, ALTER, INDEX, INSERT, DELETE, SELECT
  ON trickster.* TO 'seeder'@'%';

-- trickster: used by Trickster's upstream connection to the MySQL origin.
-- Read-only.
CREATE USER IF NOT EXISTS 'trickster'@'%' IDENTIFIED BY 'trickster-dev-upstream';
GRANT SELECT ON trickster.* TO 'trickster'@'%';

-- grafana_ro: used by the provisioned Grafana MySQL data source. Read-only.
CREATE USER IF NOT EXISTS 'grafana_ro'@'%' IDENTIFIED BY 'trickster-dev-grafana';
GRANT SELECT ON trickster.* TO 'grafana_ro'@'%';

FLUSH PRIVILEGES;
