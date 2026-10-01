# Release keys

`root.pub` is the pinned public half of the offline BAFT release root key.
Installers and agents trust a release only if its signing key carries a
certificate from this root. Nothing private ever goes in this directory.

The root key itself stays offline. See `docs/en/23-p1a-signed-releases.md`
for the key ceremony, rotation and revocation steps.
