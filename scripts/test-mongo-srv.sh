#!/usr/bin/env bash
# Tests mongodb+srv:// URLs the way Atlas serves them: a DNS server with the
# SRV and TXT records, and a replica set with a user behind them. The test
# binary runs in a container that uses that DNS server, as the host's
# resolver can't be pointed at it without root.
# Usage: scripts/test-mongo-srv.sh    (MONGODB_IMAGE picks the server)
set -euo pipefail
cd "$(dirname "$0")/.."

MONGODB_IMAGE=${MONGODB_IMAGE:-mongo:7}
DOCKER=${DOCKER:-$(command -v docker || command -v podman)}
NET=gotable-srv
PW=gotable-test
TMP=$(mktemp -d)

cleanup() {
	"$DOCKER" rm -f -v gotable-srv-dns gotable-srv-mongo gotable-srv-test >/dev/null 2>&1 || true
	"$DOCKER" network rm "$NET" >/dev/null 2>&1 || true
	rm -rf "$TMP"
}
trap cleanup EXIT
cleanup
TMP=$(mktemp -d)
mkdir "$TMP/dns" "$TMP/k" "$TMP/t" # copied in whole: docker cp makes no parents

"$DOCKER" network create --subnet 172.29.53.0/24 "$NET" >/dev/null

# The zone: cluster0.gotable.test lists one member and, as Atlas does, sets
# the replica set and authSource in TXT.
cat >"$TMP/dns/Corefile" <<'EOF'
gotable.test {
	file /dns/zone
}
EOF
cat >"$TMP/dns/zone" <<'EOF'
$ORIGIN gotable.test.
@        3600 IN SOA ns.gotable.test. admin.gotable.test. 1 7200 3600 1209600 3600
mongo1   3600 IN A   172.29.53.10
_mongodb._tcp.cluster0 3600 IN SRV 0 5 27017 mongo1.gotable.test.
cluster0 3600 IN TXT "authSource=admin&replicaSet=rs0"
EOF
"$DOCKER" create --name gotable-srv-dns --network "$NET" --ip 172.29.53.2 coredns/coredns:1.11.3 -conf /dns/Corefile >/dev/null
"$DOCKER" cp "$TMP/dns" gotable-srv-dns:/dns
"$DOCKER" start gotable-srv-dns >/dev/null

# A replica set with a user needs a key file, readable by mongod alone.
openssl rand -base64 48 >"$TMP/k/key"
"$DOCKER" create --name gotable-srv-mongo --network "$NET" --ip 172.29.53.10 --hostname mongo1.gotable.test \
	-e MONGO_INITDB_ROOT_USERNAME=gotable -e MONGO_INITDB_ROOT_PASSWORD=$PW --entrypoint bash "$MONGODB_IMAGE" \
	-c 'install -o mongodb -m 400 /k/key /tmp/key && exec docker-entrypoint.sh mongod --replSet rs0 --keyFile /tmp/key --bind_ip_all' >/dev/null
"$DOCKER" cp "$TMP/k" gotable-srv-mongo:/k
"$DOCKER" start gotable-srv-mongo >/dev/null
for i in $(seq 1 120); do
	if "$DOCKER" exec gotable-srv-mongo sh -c "\$(command -v mongosh || command -v mongo) --quiet -u gotable -p $PW --eval '
		try { rs.status().ok || rs.initiate({ _id: \"rs0\", members: [{ _id: 0, host: \"mongo1.gotable.test:27017\" }] }) }
		catch (e) { rs.initiate({ _id: \"rs0\", members: [{ _id: 0, host: \"mongo1.gotable.test:27017\" }] }) }
		db.isMaster().ismaster || quit(1)'" >/dev/null 2>&1; then break; fi
	[[ $i == 120 ]] && { echo "the replica set did not start" >&2; "$DOCKER" logs gotable-srv-mongo 2>&1 | tail -20 >&2; exit 1; }
	sleep 1
done

CGO_ENABLED=0 go test -c -o "$TMP/t/db.test" ./internal/db
"$DOCKER" create --name gotable-srv-test --network "$NET" --dns 172.29.53.2 \
	-e GOTABLE_TEST_MONGOSRV_HOST=cluster0.gotable.test -e GOTABLE_TEST_MONGOSRV_USER=gotable -e GOTABLE_TEST_MONGOSRV_PASSWORD=$PW \
	alpine:3.20 /t/db.test -test.run 'TestMongoDBSRV' -test.count=1 "${@:--test.v}" >/dev/null
"$DOCKER" cp "$TMP/t" gotable-srv-test:/t
"$DOCKER" start -a gotable-srv-test
