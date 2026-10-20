# Legal & operational notes (Thailand)

This is general information, **not legal advice**. Confirm with a Thai lawyer and
your ISP before selling access.

- **Your IP is the one that gets traced.** All customer traffic exits through your
  home/office connection, so any abuse appears to come from you. Know your
  customers and keep records.
- **Keep connection logs.** GOST already logs user, destination, bytes and
  timestamps as JSON. Retaining logs (commonly 90 days is cited for Thai
  computer-related law / PDPA-adjacent practice — verify the current requirement)
  lets you respond to complaints. Store them off the device, on the VPS.
- **Customer identification.** Require account registration (the admin-api users)
  and, for real sales, some form of KYC appropriate to your risk.
- **ISP terms.** Residential plans often prohibit reselling bandwidth. A business
  internet package is usually required.
- **PDPA.** If you store customer personal data, follow Thailand's PDPA
  (lawful basis, security, retention, subject rights).
- **Abuse controls in place:** per-user auth, rate limits, monthly quotas,
  blocked private-IP targets, blocked SMTP ports. Consider adding destination
  allow/deny lists for higher-risk categories.

The platform gives you the technical controls (auth, limits, quotas, logging) to
operate responsibly; the legal/operational obligations are yours.
