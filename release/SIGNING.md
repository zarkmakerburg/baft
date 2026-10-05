# BAFT release signing boundary

GitHub Actions builds **unsigned candidates only**. It must never receive the long-lived BAFT release signing key or release-key certificate.

## Required signing process

1. Verify the candidate tag and commit are the accepted release SHA on `main`.
2. Download the `unsigned-release-candidate` artifact and verify the embedded `CANDIDATE` file matches that tag and commit.
3. Use an independently reviewed signer binary/source at an immutable trusted commit. Do **not** execute signer code from the candidate release checkout merely because it is being signed.
4. Load the release signing key only inside the isolated/offline signing environment.
5. Sign the candidate distribution, verify it against `root.pub` and the current revocation list, then build/verify the offline bundle.
6. Publish only the verified signed artifacts. Never publish the unsigned candidate artifact as a BAFT release.

A future HSM/KMS/OIDC signing service may automate these steps only if repository-controlled release code cannot access or exfiltrate the signing authority.
