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
#   oracle   Oracle 23ai Free (~1.5 GB image)
#   ch       ClickHouse     percona   Percona Server (MySQL)
#   mongodb  MongoDB        mongors   MongoDB replica set (transactions)
#            mongoshard  MongoDB sharded cluster: mongos, a config server
#                        and two shards in one container
#            mongotls    MongoDB that requires TLS, with a test CA and a
#                        client certificate for X.509 login (needs openssl)
#   redis    Redis          valkey    Valkey (the Redis fork)
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
ORACLE_IMAGE=${ORACLE_IMAGE:-gvenzl/oracle-free:23-slim-faststart}
MONGODB_IMAGE=${MONGODB_IMAGE:-mongo:7}
REDIS_IMAGE=${REDIS_IMAGE:-redis:7-alpine}
VALKEY_IMAGE=${VALKEY_IMAGE:-valkey/valkey:8-alpine}
DOCKER=${DOCKER:-$(command -v docker || command -v podman)}
PW=gotable-test
MSSQL_PW=GoTable-test1 # SQL Server wants upper case, lower case and digits
ALL="pg mysql mariadb mssql oracle ch mongodb mongors mongoshard mongotls redis valkey crdb yb ts tidb percona s2"
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
has mongodb && run mongodb -e MONGO_INITDB_ROOT_USERNAME=gotable -e MONGO_INITDB_ROOT_PASSWORD=$PW -p 127.0.0.1::27017 "$MONGODB_IMAGE"
# A one-member replica set; without a user, as auth would need a key file.
has mongors && run mongors -p 127.0.0.1::27017 "$MONGODB_IMAGE" --replSet rs0 --bind_ip_all
# Every process listens on localhost: members only meet each other, and
# GoTable talks to mongos alone.
has mongoshard && run mongoshard -p 127.0.0.1::27017 --entrypoint bash "$MONGODB_IMAGE" -c '
	set -e
	sh=$(command -v mongosh || command -v mongo)
	start() { mkdir -p /data/$1; mongod --dbpath /data/$1 --port $2 --replSet $1 ${3:-} --fork --logpath /data/$1.log >/dev/null; }
	init() { until $sh --quiet --port $2 --eval "rs.initiate({ _id: \"$1\", ${3:-} members: [{ _id: 0, host: \"localhost:$2\" }] })"; do sleep 1; done; }
	start cfg 27019 --configsvr; start sh0 27020 --shardsvr; start sh1 27021 --shardsvr
	init cfg 27019 "configsvr: true,"; init sh0 27020; init sh1 27021
	mongos --configdb cfg/localhost:27019 --port 27018 --bind_ip localhost --fork --logpath /data/mongos.log >/dev/null
	until $sh --quiet --port 27018 --eval "sh.addShard(\"sh0/localhost:27020\"); sh.addShard(\"sh1/localhost:27021\")"; do sleep 1; done
	touch /data/ready
	# The published port: mongos again, on every address.
	exec mongos --configdb cfg/localhost:27019 --port 27017 --bind_ip_all'
if has mongotls; then
	# A CA of its own, a server certificate for localhost and 127.0.0.1, and a
	# client one whose subject is a user in $external. Copied in rather than
	# mounted, so file ownership and SELinux labels don't matter.
	CERTS=$(mktemp -d)
	trap 'cleanup; [[ "${KEEP:-}" == 1 ]] || rm -rf "$CERTS"' EXIT # the files outlive KEEP=1 containers
	(
		cd "$CERTS"
		ossl() { openssl "$@" 2>/dev/null; }
		ossl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=GoTable Test CA" -keyout ca.key -out ca.pem
		ossl req -newkey rsa:2048 -nodes -subj "/CN=localhost/O=GoTable Server" -keyout server.key -out server.csr
		printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' >server.ext
		ossl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 2 -extfile server.ext -out server.crt
		cat server.crt server.key >server.pem
		ossl req -newkey rsa:2048 -nodes -subj "/CN=gotable-client/O=GoTable Client" -keyout client.key -out client.csr
		ossl x509 -req -in client.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 2 -out client.crt
		cat client.crt client.key >client.pem
		chmod 644 ./*
		chmod 755 . # docker cp keeps the mode, and mongod is not root
	)
	"$DOCKER" create --name gotable-mongotls -e MONGO_INITDB_ROOT_USERNAME=gotable -e MONGO_INITDB_ROOT_PASSWORD=$PW \
		-p 127.0.0.1::27017 "$MONGODB_IMAGE" --tlsMode requireTLS --tlsCertificateKeyFile /certs/server.pem \
		--tlsCAFile /certs/ca.pem --tlsAllowConnectionsWithoutCertificates >/dev/null
	"$DOCKER" cp "$CERTS/." gotable-mongotls:/certs
	"$DOCKER" start gotable-mongotls >/dev/null
fi
# With a password, as a server reachable from elsewhere should have.
has redis && run redis -p 127.0.0.1::6379 "$REDIS_IMAGE" redis-server --requirepass "$PW"
has valkey && run valkey -p 127.0.0.1::6379 "$VALKEY_IMAGE" valkey-server --requirepass "$PW"
has ts && run ts -e POSTGRES_PASSWORD=$PW -p 127.0.0.1::5432 "$TS_IMAGE"
has percona && run percona -e MYSQL_ROOT_PASSWORD=$PW -e MYSQL_DATABASE=gotable -p 127.0.0.1::3306 "$PERCONA_IMAGE"
has s2 && run s2 -e ROOT_PASSWORD=$PW -p 127.0.0.1::3306 "$S2_IMAGE"
has oracle && run oracle -e ORACLE_PASSWORD=$PW -e APP_USER=gotable -e APP_USER_PASSWORD=$PW -p 127.0.0.1::1521 "$ORACLE_IMAGE"

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
if has mongodb; then
	# Images before 6.0 have only the legacy mongo shell.
	mongo_ping() { in_container mongodb sh -c "\$(command -v mongosh || command -v mongo) --quiet -u gotable -p $PW --eval 'db.runCommand({ ping: 1 })'"; }
	wait_ready mongodb mongo_ping
	export_env MONGODB "$(port mongodb 27017)" gotable $PW gotable
fi
if has mongors; then
	mongors_init() { in_container mongors sh -c "\$(command -v mongosh || command -v mongo) --quiet --eval 'try { rs.status().ok || rs.initiate() } catch (e) { rs.initiate() }; db.isMaster().ismaster || quit(1)'"; }
	wait_ready mongors mongors_init
	export_env MONGORS "$(port mongors 27017)" "" "" gotable
	export GOTABLE_TEST_MONGORS_OPTIONS=directConnection=true # the member calls itself by the container's name
fi
if has mongoshard; then
	wait_ready mongoshard in_container mongoshard sh -c 'test -e /data/ready && $(command -v mongosh || command -v mongo) --quiet --eval "db.adminCommand({ listShards: 1 }).shards.length == 2 || quit(1)"'
	export_env MONGOSHARD "$(port mongoshard 27017)" "" "" gotable
fi
if has mongotls; then
	mongotls_sh() { in_container mongotls sh -c "\$(command -v mongosh || command -v mongo) --quiet --tls --host localhost --tlsCAFile /certs/ca.pem -u gotable -p $PW --eval '$1'"; }
	wait_ready mongotls mongotls_sh 'db.runCommand({ ping: 1 })'
	mongotls_sh 'db.getSiblingDB("$external").runCommand({ createUser: "O=GoTable Client,CN=gotable-client", roles: [{ role: "root", db: "admin" }] })' >/dev/null
	export_env MONGOTLS "$(port mongotls 27017)" gotable $PW gotable
	export GOTABLE_TEST_MONGOTLS_CA=$CERTS/ca.pem GOTABLE_TEST_MONGOTLS_CLIENT=$CERTS/client.pem
fi
if has redis; then
	wait_ready redis in_container redis redis-cli -a "$PW" --no-auth-warning ping
	export_env REDIS "$(port redis 6379)" "" "$PW" 0
fi
if has valkey; then
	wait_ready valkey in_container valkey valkey-cli -a "$PW" --no-auth-warning ping
	export_env VALKEY "$(port valkey 6379)" "" "$PW" 0
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

if has oracle; then
	wait_ready oracle in_container oracle healthcheck.sh
	export_env ORACLE "$(port oracle 1521)" gotable $PW FREEPDB1
fi

if [[ "${KEEP:-}" == 1 ]]; then
	env | grep '^GOTABLE_TEST_' | sed 's/^/export /'
fi
go test -count=1 "${@:-./...}"
