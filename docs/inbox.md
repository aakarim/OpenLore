# HTTP Inbox Uploads

Configure a docset `inbox` and a role with its `publish` grant, then create a
credential for an existing identity (the server configuration must name
`auth_file` so the CLI can validate it):

```bash
openlore inbox token create --identity alice --label webhook --config openlore.yml
curl -H 'Authorization: Bearer olin_ID_SECRET' -H 'Content-Type: text/markdown' \
  --data-binary @note.md 'https://docs.example.com/inbox/docs?name=note.md'
```

`POST /inbox/{docset}` accepts bearer credentials or an exact-body HMAC using
`X-OpenLore-Token-Id` and `X-OpenLore-Signature`. OAuth access tokens are used
only for `POST/GET /inbox/tokens` and `DELETE /inbox/tokens/{id}`; inbox
credentials are separate and revocable. See
[Configuration and identity](configuration-and-identity.md#http-inbox-credentials).
