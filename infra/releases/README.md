# Release manifests

No real cloud release exists yet. Never fabricate registry digests, commit IDs,
test evidence or environment approval records merely to fill a template.

A reviewed manifest records the built source `commit`, an `images` object with
`control`, `capabilities`, `frontend` and `nats` immutable registry references, and
`verified_environments`. Each verified environment records its actual image map
and an `evidence_uri` to independently reviewable test/rollout evidence. The
promotion validator requires dev evidence before staging, and staging evidence
before production, for the same image map. Presence of a URI is a structural
check; the environment reviewer must verify its contents and authenticity.

The quality workflow builds and tests once, retains the exact image archive and
its checksum, and never publishes to a registry. An authorized release operator
can load that verified archive and publish it to the private ACR without rebuilding,
then record the actual registry digests. The promotion workflow produces a review
candidate only; it does not sync Argo, apply infrastructure or mutate Git remotes.

These GitHub workflows are configured but have never run in a remote repository.
No GitHub upload, remote Git configuration or push was executed locally.
