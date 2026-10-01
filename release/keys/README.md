# Release keys

`root.pub` is the pinned public half of the offline BAFT release root key.
Installers and agents trust a release only if its signing key carries a
certificate from this root. `revocations.json` is the current root-signed
revocation list; it is mandatory, expires, and must be re-signed (with
`baft-release revoke -in`) before then. Nothing private ever goes in this directory.

The root key itself stays offline. See `docs/en/23-p1a-signed-releases.md`
for the key ceremony, rotation and revocation steps.
