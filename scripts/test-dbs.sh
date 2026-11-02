#!/usr/bin/env bash
# Starts database containers and runs the full test suite against them.
# Usage: scripts/test-dbs.sh [go test args...]
#
# ENGINES picks the servers (default: pg mysql mariadb). The compatible ones
# are opt-in, e.g. ENGINES="pg mysql mariadb crdb yb tidb" or ENGINES=tidb.
#   pg     PostgreSQL      crdb  CockroachDB (PostgreSQL protocol)
#   mysql  MySQL           yb    YugabyteDB  (PostgreSQL protocol, ~2 GB image)
#   mariadb MariaDB        tidb  TiDB        (MySQL protocol)
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
DOCKER=${DOCKER:-$(command -v docker || command -v podman)}
PW=gotable-test
ALL="pg mysql mariadb crdb yb tidb"

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

if [[ "${KEEP:-}" == 1 ]]; then
	env | grep '^GOTABLE_TEST_' | sed 's/^/export /'
fi
go test -count=1 "${@:-./...}"
