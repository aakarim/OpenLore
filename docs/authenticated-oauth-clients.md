# Authenticated OAuth Clients

OpenLore supports OAuth 2.1 Client ID Metadata Documents (CIMD). An HTTPS URL
used as `client_id` is fetched with private-network, redirect, response-size,
and timeout guards. Its `client_name` and registrable origin produce a stable
delegated identity such as `chatgpt@chatgpt.com` or
`claude-code@claude.ai`. Dynamic Client Registration remains available for
clients without CIMD.

If a CIMD advertises `private_key_jwt`, OpenLore requires a signed client
assertion at `/oauth/token` and verifies it against the document's same-origin
`jwks_uri`. A client cannot downgrade to public-client authentication. Public
CIMD clients continue to use authorization code + PKCE. Loopback redirects from
CIMD native clients match the registered scheme, host, path, and query while
allowing the callback port to vary.

Tokens and durable write provenance record the achieved client authentication
level: `private_key_jwt+mtls`, `private_key_jwt`, `cimd`, `dcr-domain`, or
`dcr-local`.

## Optional mTLS corroboration

When OpenLore terminates TLS, it can verify client certificates if presented:

```yaml
tls_cert: /etc/openlore/server.pem
tls_key: /etc/openlore/server-key.pem
auth:
  mtls:
    ca_bundle: /etc/openlore/openai-connectors-ca.pem
```

This is best-effort corroboration: clients that do not present a certificate
can still connect. A presented certificate must chain to the configured CA.
OpenLore does not match the certificate SAN to the CIMD client, so `ca_bundle`
must contain only the CA certificates for the vendor whose mTLS corroboration
OpenLore should record. Do not use a general-purpose client CA bundle.
Reverse-proxy certificate forwarding is not supported.

## Signing-key rotation

OpenLore retains the previous ES256 public key for the configured maximum access
token lifetime, so normal rotation does not interrupt active clients:

```sh
openlore oauth keys rotate --config ./openlore.yml
```

For key compromise, remove every prior key immediately:

```sh
openlore oauth keys rotate --revoke-previous --config ./openlore.yml
```

The server observes CLI rotations without restarting. Clients can disconnect by
posting their refresh token to `/oauth/revoke`; OpenLore revokes the full refresh
chain, while already-issued access tokens expire at their normal short TTL.
