# Incident — 2026-10-09 mainland release: `.41` cancelled, `.42` gate failure, rollback

Status: **open** (`.42` is not serving; the relay change is in git but not in production).
Author: dsh (night watch). Every claim below is tagged 已证实 / 推断 / 待验证.

## Summary

Two release attempts on 2026-10-09 evening, neither reached production:

| Tag | Commit | Outcome |
|---|---|---|
| `v1.0.0-rc.40-tokeness-mainland.41` | `84d9b7abb` | **cancelled mid-deploy** — the tag predated the deploy-tooling fix by 5 seconds, so it ran the racy `deploy.sh` |
| `v1.0.0-rc.40-tokeness-mainland.42` | `7d35480ae` | **gate failure at 20:38, auto-rolled back** — a batch instance did not start serving within 180s |

Production is serving `.41` on the master and mostly `.42` on the relay fleet; behaviour is
equivalent between the two (the only relay difference is a per-channel flag that defaults off).

## `.41` — the five-second race (已证实)

```
18:43:50  tag .41 pushed            -> points at 84d9b7abb
18:43:55  7ad8abec8 committed       fix(deploy): a scale-in is asynchronous, so gate on an exact instance count
```

The pipeline pins the deploy tooling to the tagged commit, so `.41` used the racy revision that
aborted `.40` and would have aborted again. Both builds were stopped through the CNB console
(`cnb-7vc-1k4g4bg8h` the deploy, `cnb-27a-1k4g46503` the image build); the deploy had already
reached the ESS stage, which is why one instance briefly ran `.41`.

**Lesson**: never tag while the deploy tooling is being fixed. Tag only after the tooling commit
is on the branch and the branch validation is green.

## `.42` — batch gate failure (已证实 for the sequence, 推断 for the cause)

CNB deploy log (`cnb-p74-1k4g4oifa`):

```
18:54:47  master blue-green complete: :3000 -> :3001 (version .42)
18:54:48  steady-state capacity is 7; rolling out through 8 instances
19:27:45  ess rollout to sha256:f4f44e8... complete
20:38:20  ERROR: rollout: the ECS relay tier did not start serving 10.0.0.22 within 180s
20:38:25  rollback verified; serving the previous image
20:38:53  master blue-green complete: :3001 -> :3000 (version .41)
20:38:53  ERROR: release batch rollout failed
```

ESS scaling activities around the failure:

```
20:33:47  Desired -> 7 ; Remove "2"
20:33:48  Desired -> 9 ; Add "2"
20:34:27  Add "2" ok
20:38:24  Desired -> 7 ; Remove "2"          <- rollback
20:41:12  health check task: Remove "1"      <- cleans up the broken instance, AFTER the failure
20:41:13  Add "1" (image .41)
```

- 已证实: `10.0.0.22` never served and no longer exists; its logs are gone with it.
- 推断: the instance needed more than 180s to come up (cold image layer / ECI scheduling).
  Cause is **not** proven and cannot be reconstructed.
- 已证实: the health-check removal happened *after* the gate failure, so the health check is
  cleanup here, not the cause.
- 已证实: the rollback restored the capacity it read at the start (7), not an original 6 — this is
  why `DesiredCapacity` is 7 today and why the earlier cancelled `.41` run left it there too.

## Facts about the ECI tier learned here (已证实)

Read with `ess DescribeScalingGroups --ScalingGroupId asg-uf641n1j5akwa1ozcz6t`:

```
HealthCheckType        = ECS             the group health-checks instances itself
HealthCheckGracePeriod = not set         new instances are checked immediately
RemovalPolicies        = OldestInstance  scale-in retires the oldest instance first
DesiredCapacity = 7  MinSize = 2  MaxSize = 10
```

- The tier is **self-healing**: a broken instance was removed and replaced automatically.
- The tier **converges to whatever image the scaling configuration pins**. After the rollback the
  configuration pins `.41`, so instances replaced from now on come up as `.41` and the current
  mixed fleet resolves itself — no manual convergence is needed.
- **No health-check grace period** is the one standing risk worth fixing: a slow-booting instance
  during a rollout can be removed by the health check. Recommended: set a grace period of ~600s.

## How to retry (recommendation, not executed)

1. Re-run the existing `.42` build rather than pushing a new tag — the tag already points at
   `7d35480ae`, which contains the deploy-tooling fix, and the image is warm on six instances now.
2. Optionally set `HealthCheckGracePeriod` on the scaling group first.
3. Do not run `deploy.sh rollout-all` by hand while a build is in flight: it reads the *current*
   capacity as the steady state, so a fleet left at 7 by an aborted run makes 7 the new normal.

## Production impact (已证实)

Entry-layer access log (`canary.log`, 19:46–20:46, 11,481 requests): `200` x 11,481, `401` x 506
(invalid-key probes), `400` x 7, `502` x 6 = **0.052%**, below the 0.771% baseline. The 502s are
churn artefacts (two at 20:03 on ~798s requests, two at 20:36/20:41 during the rollout).
