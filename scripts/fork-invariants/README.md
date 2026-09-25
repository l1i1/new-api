# Fork invariants gate

`/scripts/fork-invariants/main.mjs` is the executable form of the upstream-sync
discipline in [`AGENTS.md`](../../AGENTS.md#upstream-sync-rcnn-merges). The
ritual it replaces failed twice with an all-green test suite:

- **rc35** silently dropped five fork changes while resolving conflicts.
- **rc39** dropped the whole video-token hardening block.

Both times every gate stayed green, because "the tests pass" cannot tell you
that a fork change is *gone*. These checks can.

```bash
node scripts/fork-invariants/main.mjs                       # all checks
node scripts/fork-invariants/main.mjs --check i18n          # one check
node scripts/fork-invariants/main.mjs --check merge --merge <sha>
node scripts/fork-invariants/main.mjs --json                # machine-readable
node --test scripts/fork-invariants/main.test.mjs           # tests for the gate itself
```

Exit code is `0` when every requested check passes, `1` otherwise. A full run
takes ~10 s on a laptop and needs no network (see *Offline by design*).

## The checks

| check | blocks | what it proves |
| --- | --- | --- |
| `manifest` | yes | Every fork change inventoried in `FORK-CHANGES.md` still has its anchors (key files, fork-unique symbols, test names) in the tree. |
| `i18n` | yes | `web/src/i18n/locales/*.json` are still upstream-owned bytes, and the fork overlay (`web/src/i18n/overlay/`) is well-formed. |
| `orphans` | yes | No **new** dead producer appeared: an exported Go symbol nothing references, or an overlay locale key no frontend file mentions. A merge that drops a *consumer* shows up here even when the producer survived. |
| `merge` | yes | For a merge commit, no path where the fork side had added lines was resolved by taking upstream's file wholesale, and the merge deleted no fork file. |
| `history` | advisory | Lines added by fork-only commits are still present. A low ratio is often legitimate (the code was rewritten), so this reports rather than blocks unless `--fail-on-history-drop` is passed. |

`merge` on an ordinary (non-merge) `HEAD` reports `not a merge commit` and
passes; on the rcNN sync PR it is the check that matters.

## Files

| file | role |
| --- | --- |
| `manifest.json` | the survival list, one entry per `FORK-CHANGES.md` row: `files`, `anchors` (`name` symbol / `contains` literal / `regex`), `tests`. `status: absorbed-upstream` marks an entry upstream has since shipped itself. |
| `upstream-locales.json` | sha256 of each upstream-owned locale bundle plus the upstream facts the offline run needs (key-set hash, key count, and any collision between an upstream key and a fork overlay key). |
| `orphan-baseline.json` | accepted orphans at the time it was recorded. The gate fails only on *new* ones. |
| `merge-allowlist.json` | paths where taking upstream's side was deliberate. Entries need a reason and stay visible in the output as *acknowledged*. |
| `seed-manifest.mjs` | drafts `manifest.json` from the `FORK-CHANGES.md` tables (fork-unique anchors). Output is a draft: review it, then commit. |

## Fixing a failure

- **`manifest`: an anchor is gone** — the merge reverted that fork change.
  Restore it from the fork parent (`git checkout <fork-parent> -- <file>`) or,
  if upstream superseded it, mark the entry `status: absorbed-upstream` with the
  reason in `supersededBy`. Never just delete the anchor.
- **`i18n`: bundle edited** — move the change into `web/src/i18n/overlay/`
  (`translation` for keys upstream does not ship, `overrides` for keys we word
  differently) and restore the bundle. On a sync that legitimately takes
  upstream's bundle, re-record: `--record-locales`.
- **`i18n`: upstream now ships a fork-only key** — upstream adopted the same
  key. Decide: delete it from the overlay (upstream's wording wins) or move it
  to `overrides` (ours wins). Then re-record.
- **`orphans`: a new orphan** — find the consumer the merge dropped. If the
  symbol is genuinely dead, delete the symbol *and* the dead locale key instead
  of extending the baseline; extend the baseline only for things a grep cannot
  see (registration by reflection, keys composed at runtime) with
  `--update-baseline`.
- **`merge`: a flagged path** — check the fork parent's version by hand. If the
  behaviour moved elsewhere, add the path to `merge-allowlist.json` with the
  reason; if it was lost, restore it.

## Offline by design

`forkKeyCollisions`, `staleOverrides` and the bundle hashes are recorded from
upstream at sync time, so CI does not need to fetch `QuantumNous/new-api` and
cannot be broken by an upstream outage. When `--upstream <ref>` *is* resolvable
(a local run, or the sync PR), the same facts are recomputed live and checked
against the working tree.

## When fork behaviour changes

1. Update `FORK-CHANGES.md` (its §10 requires the entry and the code in the same
   commit).
2. Run `node scripts/fork-invariants/seed-manifest.mjs`, keep the new entry's
   anchors, re-add hand-written ones (the seeder overwrites the file).
3. Run the gate; commit both together.
