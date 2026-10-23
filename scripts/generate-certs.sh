#!/usr/bin/env bash
set -euo pipefail

CERT_DIR="${1:-certs}"
# Override the server SAN via environment variable.
# Default: localhost + in-cluster DNS for the replica-manager-test and default namespaces.
SERVER_SAN="${CERT_SAN:-DNS:localhost,DNS:replica-manager.replica-manager-test.svc.cluster.local,DNS:replica-manager.default.svc.cluster.local,IP:127.0.0.1}"
mkdir -p "$CERT_DIR"

# Skip if CA already exists
if [ -f "$CERT_DIR/ca.pem" ]; then
	echo "CA cert already exists at $CERT_DIR/ca.pem — skipping cert generation"
	exit 0
fi

# CA key + self-signed cert
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:secp384r1 \
	-out "$CERT_DIR/ca-private.pem"
openssl req -x509 -new -nodes -key "$CERT_DIR/ca-private.pem" \
	-days 3650 -sha384 \
	-subj "/CN=Replica Manager CA" \
	-out "$CERT_DIR/ca.pem"

# Server key + CSR + cert signed by CA
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:secp384r1 \
	-out "$CERT_DIR/server-private.pem"
openssl req -new -key "$CERT_DIR/server-private.pem" \
	-subj "/CN=localhost" \
	-addext "subjectAltName=${SERVER_SAN}" \
	-out "$CERT_DIR/server.csr"
openssl x509 -req -in "$CERT_DIR/server.csr" \
	-CA "$CERT_DIR/ca.pem" -CAkey "$CERT_DIR/ca-private.pem" \
	-CAcreateserial -days 365 -sha384 \
	-extfile <(printf "subjectAltName=${SERVER_SAN}") \
	-out "$CERT_DIR/server-public.pem"
rm -f "$CERT_DIR/server.csr"

# Client key + CSR + cert signed by CA
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:secp384r1 \
	-out "$CERT_DIR/client-private.pem"
openssl req -new -key "$CERT_DIR/client-private.pem" \
	-subj "/CN=client" -out "$CERT_DIR/client.csr"
openssl x509 -req -in "$CERT_DIR/client.csr" \
	-CA "$CERT_DIR/ca.pem" -CAkey "$CERT_DIR/ca-private.pem" \
	-CAcreateserial -days 365 -sha384 \
	-out "$CERT_DIR/client-public.pem"
rm -f "$CERT_DIR/client.csr" "$CERT_DIR/*.srl"

echo "Certificates generated in $CERT_DIR/"
echo "  ca.pem, ca-private.pem — CA"
echo "  server-public.pem, server-private.pem — server"
echo "  client-public.pem, client-private.pem — client"