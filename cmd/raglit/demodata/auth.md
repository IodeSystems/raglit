# Authentication

Access tokens are short-lived and expire 15 minutes after issue. When a token
expires, the client exchanges its refresh token for a new access token.

Refresh tokens rotate on every use: each exchange returns a brand-new refresh
token and invalidates the old one. A refresh token that is reused after
rotation is treated as a compromise and revokes the whole session.

Sessions idle out after 30 days without any refresh.
