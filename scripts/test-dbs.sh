#!/usr/bin/env bash
# Starts PostgreSQL, MySQL and MariaDB containers and runs the full test
# suite against them. Usage: scripts/test-dbs.sh [go test args...]
# Set KEEP=1 to leave the containers running afterwards.
set -euo pipefail
cd "$(dirname "$0")/.."

PG_IMAGE=${PG_IMAGE:-postgres:15-alpine}
MYSQL_IMAGE=${MYSQL_IMAGE:-mysql:8.4}
MARIADB_IMAGE=${MARIADB_IMAGE:-mariadb:11}
PW=gotable-test

cleanup() {
	if [[ "${KEEP:-}" != 1 ]]; then
		docker rm -f -v gotable-pg gotable-mysql gotable-mariadb >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT
cleanup

docker run -d --name gotable-pg -e POSTGRES_PASSWORD=$PW -p 127.0.0.1::5432 "$PG_IMAGE" >/dev/null
docker run -d --name gotable-mysql -e MYSQL_ROOT_PASSWORD=$PW -e MYSQL_DATABASE=gotable -p 127.0.0.1::3306 "$MYSQL_IMAGE" >/dev/null
docker run -d --name gotable-mariadb -e MARIADB_ROOT_PASSWORD=$PW -e MARIADB_DATABASE=gotable -p 127.0.0.1::3306 "$MARIADB_IMAGE" >/dev/null

port() { docker port "$1" "$2" | head -1 | sed 's/.*://'; }

wait_ready() {
	local name=$1; shift
	for _ in $(seq 1 90); do
		if docker exec "$name" "$@" >/dev/null 2>&1; then return 0; fi
		sleep 1
	done
	echo "$name did not become ready" >&2
	docker logs "$name" | tail -20 >&2
	return 1
}
wait_ready gotable-pg pg_isready -U postgres -h 127.0.0.1
wait_ready gotable-mysql mysql -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'
wait_ready gotable-mariadb mariadb -uroot -p$PW -h127.0.0.1 -e 'SELECT 1'

export GOTABLE_TEST_PG_HOST=127.0.0.1 GOTABLE_TEST_PG_PORT=$(port gotable-pg 5432)
export GOTABLE_TEST_PG_USER=postgres GOTABLE_TEST_PG_PASSWORD=$PW GOTABLE_TEST_PG_DB=postgres
export GOTABLE_TEST_MYSQL_HOST=127.0.0.1 GOTABLE_TEST_MYSQL_PORT=$(port gotable-mysql 3306)
export GOTABLE_TEST_MYSQL_USER=root GOTABLE_TEST_MYSQL_PASSWORD=$PW GOTABLE_TEST_MYSQL_DB=gotable
export GOTABLE_TEST_MARIADB_HOST=127.0.0.1 GOTABLE_TEST_MARIADB_PORT=$(port gotable-mariadb 3306)
export GOTABLE_TEST_MARIADB_USER=root GOTABLE_TEST_MARIADB_PASSWORD=$PW GOTABLE_TEST_MARIADB_DB=gotable

if [[ "${KEEP:-}" == 1 ]]; then
	env | grep '^GOTABLE_TEST_' | sed 's/^/export /'
fi
go test -count=1 "${@:-./...}"
