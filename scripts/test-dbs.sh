#!/usr/bin/env bash
# Starts database containers and runs the full test suite against them.
# Usage: scripts/test-dbs.sh [go test args...]
#
# ENGINES picks the servers (default: pg mysql mariadb); the others are
# opt-in, e.g. ENGINES="mssql ch" or ENGINES=all. DuckDB and SQLite need no
# server and always run.
#   pg       PostgreSQL     crdb      CockroachDB    (PostgreSQL protocol)
#   mysql    MySQL          yb        YugabyteDB     (PostgreSQL, ~2 GB image)
#   mariadb  MariaDB        ts        TimescaleDB    (PostgreSQL)
#   mssql    SQL Server     tidb      TiDB           (MySQL protocol)
#   ch       ClickHouse     percona   Percona Server (MySQL)
#                           s2        SingleStore    (MySQL, needs 4 GB RAM)
# Set KEEP=1 to leave the containers running afterwards.
set -euo pipefail
cd "$(dirname "$0")/.."

ENGINES=${ENGINES:-pg mysql mariadb}
PG_IMAGE=${PG_IMAGE:-postgres:15-alpine}
MYSQL_IMAGE=${MYSQL_IMAGE:-mysql:8.4}
MARIADB_IMAGE=${MARIADB_IMAGE:-mariadb:11}
CRDB_IMAGE=${CRDB_IMAGE:-cockroachdb/cockroach:v24.3.5}
YB_IMAGE=${YB_IMAGE:-yugabytedb/yugabyte:2024.2.2.1-b6}
TIDB_IMAGE=${TIDB_IMAGE:-pingcap/tidb:v8.5.1}
MSSQL_IMAGE=${MSSQL_IMAGE:-mcr.microsoft.com/mssql/server:2022-latest}
CH_IMAGE=${CH_IMAGE:-clickhouse/clickhouse-server:24.8}
TS_IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg17}
PERCONA_IMAGE=${PERCONA_IMAGE:-percona/percona-server:8.4}
S2_IMAGE=${S2_IMAGE:-ghcr.io/singlestore-labs/singlestoredb-dev:latest}
DOCKER=${DOCKER:-$(command -v docker || command -v podman)}
PW=gotable-test
MSSQL_PW=GoTable-test1 # SQL Server wants upper case, lower case and digits
ALL="pg mysql mariadb mssql ch crdb yb ts tidb percona s2"
[[ "$ENGINES" == all ]] && ENGINES=$ALL

cleanup() {
	if [[ "${KEEP:-}" != 1 ]]; then
		for e in $ALL; do "$DOCKER" rm -f -v "gotable-$e" >/dev/null 2>&1 || true; done
	fi
}
trap cleanup EXIT
cleanup

has() { [[ " $ENGINES " == *" $1 "* ]]; }
run() { local e=$1; shift; "$DOCKER" run -d --name "gotable-$e" "$@" >/dev/null; }
port() { "$DOCKER" port "gotable-$1" "$2" | head -1 | sed 's/.*://'; }

# wait_ready NAME CHECK...: runs CHECK (a host command) until it succeeds.
wait_ready() {
	local e=$1; shift
	for _ in $(seq 1 180); do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 1
	done
	echo "gotable-$e did not become ready" >&2
	"$DOCKER" logs "gotable-$e" 2>&1 | tail -20 >&2
	return 1
}
in_container() { local e=$1; shift; "$DOCKER" exec "gotable-$e" "$@"; }
tcp_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

has pg && run pg -e POSTGRES_PASSWORD=$PW -p 127.0.0.1::5432 "$PG_IMAGE"
has mysql && run mysql -e MYSQL_ROOT_PASSWORD=$PW -e MYSQL_DATABASE=gotable -p 127.0.0.1::3306 "$MYSQL_IMAGE"
has mariadb && run mariadb -e MARIADB_ROOT_PASSWORD=$PW -e MARIADB_DATABASE=gotable -p 127.0.0.1::3306 "$MARIADB_IMAGE"
has crdb && run crdb -p 127.0.0.1::26257 "$CRDB_IMAGE" start-single-node --insecure
# yugabyted listens on the address it advertises: the container's own.
has yb && run yb -p 127.0.0.1::5433 "$YB_IMAGE" bash -c 'exec bin/yugabyted start --background=false --advertise_address="$(hostname -i | cut -d" " -f1)"'
has tidb && run tidb -p 127.0.0.1::4000 "$TIDB_IMAGE"
has mssql && run mssql -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=$MSSQL_PW -p 127.0.0.1::1433 "$MSSQL_IMAGE"
has ch && run ch -e CLICKHOUSE_USER=gotable -e CLICKHOUSE_PASSWORD=$PW -e CLICKHOUSE_DB=gotable -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 -p 127.0.0.1::9000 "$CH_IMAGE"
has ts && run ts -e POSTGRES_PASSWORD=$PW -p 127.0.0.1::5432 "$TS_IMAGE"
has percona && run percona -e MYSQL_ROOT_PASSWORD=$PW -e MYSQL_DATABASE=gotable -p 127.0.0.1::3306 "$PERCONA_IMAGE"
has s2 && run s2 -e ROOT_PASSWORD=$PW -p 127.0.0.1::3306 "$S2_IMAGE"

# export_env NAME HOST_PORT USER PASSWORD DB
export_env() {
	export "GOTABLE_TEST_$1_HOST=127.0.0.1" "GOTABLE_TEST_$1_PORT=$2" "GOTABLE_TEST_$1_USER=$3" "GOTABLE_TEST_$1_PASSWORD=$4" "GOTABLE_TEST_$1_DB=$5"
}

if has pg; then
	wait_ready pg in_container pg pg_isready -U postgres -h 127.0.0.1
	export_env PG "$(port pg 5432)" postgres $PW postgres
fi
if has mysql; then
	wait_ready mysql in_container mysql mysql -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'
	export_env MYSQL "$(port mysql 3306)" root $PW gotable
fi
if has mariadb; then
	wait_ready mariadb in_container mariadb mariadb -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'
	export_env MARIADB "$(port mariadb 3306)" root $PW gotable
fi
if has crdb; then
	wait_ready crdb in_container crdb cockroach sql --insecure -e 'SELECT 1'
	export_env CRDB "$(port crdb 26257)" root "" defaultdb
fi
if has yb; then
	wait_ready yb in_container yb bash -c 'bin/ysqlsh -h "$(hostname -i | cut -d" " -f1)" -U yugabyte -c "SELECT 1"'
	export_env YB "$(port yb 5433)" yugabyte yugabyte yugabyte
fi
if has tidb; then
	wait_ready tidb tcp_open "$(port tidb 4000)"
	export_env TIDB "$(port tidb 4000)" root "" test
fi

if has mssql; then
	sqlcmd() { in_container mssql /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P "$MSSQL_PW" "$@"; }
	wait_ready mssql sqlcmd -Q 'SELECT 1'
	sqlcmd -Q "IF DB_ID('gotable') IS NULL CREATE DATABASE gotable" >/dev/null
	export_env MSSQL "$(port mssql 1433)" sa "$MSSQL_PW" gotable
fi
if has ch; then
	wait_ready ch in_container ch clickhouse-client --user gotable --password $PW -q 'SELECT 1'
	export_env CH "$(port ch 9000)" gotable $PW gotable
fi
if has ts; then
	wait_ready ts in_container ts pg_isready -U postgres -h 127.0.0.1
	sleep 3 # the image restarts PostgreSQL once after loading TimescaleDB
	wait_ready ts in_container ts pg_isready -U postgres -h 127.0.0.1
	export_env TIMESCALE "$(port ts 5432)" postgres $PW postgres
fi
if has percona; then
	wait_ready percona in_container percona mysql -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'
	export_env PERCONA "$(port percona 3306)" root $PW gotable
fi
if has s2; then
	wait_ready s2 in_container s2 singlestore -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'
	in_container s2 singlestore -uroot -p$PW -h127.0.0.1 -e 'CREATE DATABASE IF NOT EXISTS gotable' >/dev/null
	export_env SINGLESTORE "$(port s2 3306)" root $PW gotable
fi

if [[ "${KEEP:-}" == 1 ]]; then
	env | grep '^GOTABLE_TEST_' | sed 's/^/export /'
fi
go test -count=1 "${@:-./...}"
