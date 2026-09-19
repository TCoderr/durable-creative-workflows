# Contributing

The repository's technical name is `durable-creative-workflows`; the product
name is VELIN. Keep the approved visual design and owner-supplied portraits.

Future commits use `TCoderr <tsinanergun@gmail.com>` for author and committer,
with no `Co-Authored-By` or generated-by trailers.
Configure this repository locally after Git is initialized:

```sh
git config --local user.name TCoderr
git config --local user.email tsinanergun@gmail.com
```

Run the checks in [RUNBOOK.md](RUNBOOK.md) before a release. Local credentials,
test evidence, screenshots, build scratch and runtime artifacts belong in
ignored directories. Apply database changes as new migrations. Changes to
Temporal commands must preserve replay of existing histories.
