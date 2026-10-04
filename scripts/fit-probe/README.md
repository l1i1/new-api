# fit-probe — operator's guide

`scripts/fit-probe/` is a self-contained Go program (`package main`) that measures which
OpenAI-compatible capabilities an aggregator channel actually implements. It sends one minimal
request per *capability mark* through the relay, compares the response with the repository's
documented expectation table (or with a measured official baseline), and — only with `--write` —
posts the decisive rows as a *suite report* to the gateway's capability endpoint.

This document is written for the operator who is about to spend probes and possibly write marks.
Every behavioural claim carries a `file:line` citation, relative to the repository root
(`/home/vhbs/tokeness/apps/new-api`), so you can check it without reading the whole package.

**The directory is untracked in git** (`git status` → `?? scripts/fit-probe/`), so nothing here is
pinned by a commit. Pin the revision you measured with the hashes below and re-check them before
acting on a later copy — the audits in `~/workos/scratch/` learned this the hard way (§7).

Revision pin (this working copy, HEAD `be7965d79afabba45bf524c1cbc99f5146822c42`). `probe.go` and
`regression_test.go` carry the R20 deepseek determinism pin of §4.1; every other file is unchanged from the
run that produced the `~/workos/scratch/fit-official/REPORT.md` numbers:

| file | md5 |
|---|---|
| `scripts/fit-probe/main.go` | `9332c67c2c4f6a6da30e589354436663` |
| `scripts/fit-probe/probe.go` | `2672f815eb6d48726d7c642e41627162` |
| `scripts/fit-probe/spec.go` | `e74fde1f0335eac6f85d43b7f9664961` |
| `scripts/fit-probe/report.go` | `dc99cbd758d672575b66f9d9280af479` |
| `scripts/fit-probe/client.go` | `ff2c24e1584973df068bc6e72655f6d5` |
| `scripts/fit-probe/rounds.go` | `350dcf96399436cac66c788a0c3a48f3` |
| `scripts/fit-probe/payload_in.go` | `d416973e0c60f51ee46f45391c45020c` |
| `scripts/fit-probe/regression_test.go` | `2e68e3f63a905741e11ff96ec4fe8407` |
| `scripts/fit-probe/report_test.go` | `8a3ff536fb3a48980fb5f0d47464d0a8` |

---

## 1. What it measures

### 1.1 The three families

Family ids are registered in `pkg/fitpolicy/builtin.go:108-110`; the model→family mapping is by
prefix and is applied to each channel's own model list (`officialfit/officialfit.go:77-105`,
`officialfit/officialfit.go:151`, used at `scripts/fit-probe/main.go:689`). A channel is probed only
for models whose id maps to a selected family.

| family | model prefixes / official ids | marks the shipped policy can require | count |
|---|---|---|---|
| `deepseek-v4` | `deepseek-v4*` (`deepseek-v4-pro`, `deepseek-v4-flash`, `deepseek-v4-flash-vision-exp`, `deepseek-v4.1-flash`) | `logprobs.dual_path`, `image.parts`, `usage.thinking_counting` | 3 |
| `kimi-k3` | `kimi-k3*` (`kimi-k3`) | `usage.thinking_counting`, `tools.choice_semantics`, `response_format.json`, `history.assistant_first`, `tools.dynamic_names` | 5 |
| `glm-5.3` | `glm-5.3*` (`glm-5.3`, `glm-5.3-flash`) | `family.whole` | 1 |

Requirement lists: `pkg/fitpolicy/builtin.go:35-48` (deepseek-v4), `:50-67` (kimi-k3), `:69-77`
(glm-5.3).

### 1.2 The eight capability marks

Eight distinct mark names exist (`pkg/fitpolicy/builtin.go:117-124`); `usage.thinking_counting` is
required by two families, so a channel that serves one model per family yields **9 probe rows**
(3 + 5 + 1). Every probe is `POST /v1/chat/completions` with `stream:false` and a four-field
skeleton plus the mark's own fields (`scripts/fit-probe/probe.go:61-70`).

| mark | family(ies) | probe shape | discriminator tier | decidable with no measured baseline? |
|---|---|---|---|---|
| `logprobs.dual_path` | deepseek-v4 | `logprobs:true`, `top_logprobs:1`, `thinking.type=disabled`, cap 8 | structural | yes: logprob data present ⇒ consistent, absent ⇒ divergence (`scripts/fit-probe/probe.go:115-153`, `:775-800`, `:823-836`) |
| `image.parts` | deepseek-v4 | text part + 1×1 PNG data URI, cap 8 | acceptance | yes, but only acceptance-tier: an accepted response (`scripts/fit-probe/probe.go:154-166`, `:810-813`) |
| `usage.thinking_counting` (deepseek) | deepseek-v4 | `thinking.type=enabled`, cap 8 | structural | yes: `reasoning_content` present or a reported reasoning-token field ⇒ consistent (`scripts/fit-probe/probe.go:167-204`, `:832`) |
| `usage.thinking_counting` (kimi) | kimi-k3 | `thinking.type=disabled`, cap 8 | acceptance (`prompt_tokens` only) | **no** — inconclusive without a baseline (`scripts/fit-probe/spec.go:98-108`, `scripts/fit-probe/probe.go:801-803`) |
| `tools.choice_semantics` | kimi-k3 | `tools:[ping]`, `tool_choice:"required"`, cap 96 | structural | yes: `tool_calls` present ⇒ consistent, absent below the cap ⇒ divergence (`scripts/fit-probe/probe.go:205-230`, `:827-828`) |
| `response_format.json` | kimi-k3 | `response_format.json_object`, prompt contains the word `json`, cap 16 | structural | partly: JSON-object content ⇒ consistent; absent content ⇒ inconclusive, never divergence (`scripts/fit-probe/probe.go:231-254`) |
| `history.assistant_first` | kimi-k3 | assistant turn before the user turn, cap 1 | acceptance (`prompt_tokens` only) | **no** (`scripts/fit-probe/spec.go:133-143`) |
| `tools.dynamic_names` | kimi-k3 | tools carried on an empty system message, cap 1 | acceptance (`prompt_tokens` only) | **no** (`scripts/fit-probe/spec.go:144-155`) |
| `family.whole` | glm-5.3 | plain `hi`, cap 1 | acceptance | yes, but only acceptance-tier (`scripts/fit-probe/spec.go:156-168`, `scripts/fit-probe/probe.go:810-813`) |

Where the mark list comes from: a real run reads the **live** policy document from
`GET /api/fit-policy` and falls back to the shipped default document if it is unreadable or does not
parse (`scripts/fit-probe/main.go:586-609`); dry-run, offline and `--plan` always use the shipped
default (`scripts/fit-probe/main.go:587-589`). A mark the policy can require but the tool has no
minimal probe for is reported as inconclusive rather than dropped (`scripts/fit-probe/main.go:748-768`).

Every absence-derived divergence in the table above passes through the truncation guard of §4 first:
a response that looks cut off by the probe's own cap is downgraded to inconclusive, never written
(`scripts/fit-probe/probe.go:638-669`, reached from `:761-781`).

### 1.3 What a verdict means

Three verdicts exist (`scripts/fit-probe/probe.go:618-625`) and exactly two of them are serialized
(`scripts/fit-probe/report.go:151-197`):

| verdict | payload | conditions |
|---|---|---|
| `consistent` | `supported: true`, `cases: "<votes>/<rounds>"` | the channel produced the mark's evidence, or matched a measured baseline; the evidence tier must be encodable (`scripts/fit-probe/report.go:165-177`, `:204-213`) |
| `divergence` | `supported: false` | the channel rejected an attributable shape, contradicted a measured baseline, or omitted structural evidence that must be present; same tier gate (`scripts/fit-probe/report.go:178-193`) |
| `inconclusive` | **nothing** — the row is omitted from the payload | everything else (`scripts/fit-probe/report.go:194-196`) |

The design rule, quoted verbatim from `scripts/fit-probe/report.go:145-150`:

> Only decisive evidence reaches the payload: a known-tier consistent observation becomes
> `supported=true`, a known-tier divergence becomes `supported=false`, and everything else — an
> inconclusive probe, an unknown evidence tier — is omitted (an invented false is the same lie as an
> invented true).

For multi-round runs the same rule is restated at `scripts/fit-probe/rounds.go:17-19`: a tie or a
plurality "leaves the row undecided, which is carried as an inconclusive row and therefore never
serialized".

Evidence tiers: `structural` is always encodable; `acceptance` is encodable only with
`--acceptance-marks`; an empty or unknown tier is never encodable, in either verdict direction
(`scripts/fit-probe/report.go:199-213`).

**Read `--explain` if you care what was *not* written.** Without it the tool prints only the payload
(`scripts/fit-probe/main.go:558-565`), so an omitted row leaves no trace in the output except the
per-row `fit-probe: undecided ...` lines a multi-round run writes to stderr
(`scripts/fit-probe/rounds.go:173-181`). With `--explain` you get every row (`results`), the plan,
the summary counters `encoded_results`/`omitted_results`/`undecided` and the exact payload that
would be posted (`scripts/fit-probe/main.go:169-192`, `:523-544`).

---

## 2. Safety model

### 2.1 Nothing happens by default

`--dry-run` defaults to **true** (`scripts/fit-probe/main.go:91`). In that mode `offline` is forced
true (`scripts/fit-probe/main.go:254-255`) and the tool sends no request at all: channels are a
single placeholder and every row is a placeholder marked inconclusive
(`scripts/fit-probe/main.go:305-313`, `:341-362`). The other modes either stay offline or keep the
run read-only (`scripts/fit-probe/main.go:255-266`):

| mode | how you get it | network |
|---|---|---|
| `dry-run` | default, or `--dry-run` | none |
| `offline` | `--offline` | none; prints the payload shape and the planned minimal requests (`scripts/fit-probe/main.go:95`) |
| `offline (no --base-url or no admin credential)` | real run without a base URL or without the admin credential in the environment | none |
| `plan` | `--dry-run=false --plan` with a base URL and admin credential | read-only admin `GET`s only; no probe, no write (`scripts/fit-probe/main.go:96`, `:341-362`) |

The mode is printed as `mode` in `--explain` output (`scripts/fit-probe/main.go:173`, `:256-266`) — if
you asked for a plan but see `"mode": "offline (no --base-url or no admin credential)"`, the admin
credential was not visible and you are looking at the placeholder plan, not your fleet.

### 2.2 The write gate

There is exactly one write in the program: `POST {admin}/api/fit-capability/report` with the admin
credential (`scripts/fit-probe/client.go:208-211`, called only at `scripts/fit-probe/main.go:546-556`).
It is refused outright in three cases:

1. `--write` while `--dry-run` is in effect (`scripts/fit-probe/main.go:273-276`);
2. `--write` while offline — missing `--base-url` or admin credential
   (`scripts/fit-probe/main.go:277-279`);
3. `--write` when the run has **zero decisive rows** — the endpoint rejects an empty report, so the
   refusal comes after the measurement, not before (`scripts/fit-probe/main.go:459-461`; the offline
   validator agrees at `pkg/fitpolicy/report.go:96-98`).

The static refusals (1) and (2) are checked **before any probe is sent**, so a write that cannot
happen costs nothing (`scripts/fit-probe/main.go:268-280`). `--payload-in` additionally requires
`--write` (`scripts/fit-probe/main.go:281-283`). A non-200 answer from the endpoint is a hard error,
never retried (`scripts/fit-probe/main.go:552-554`).

### 2.3 `--rounds N`: majority, ties undecided

`--rounds` defaults to 1; values below 1 are rejected and the upper bound is
`fitpolicy.MaxSuiteReportRounds = 1000` (`scripts/fit-probe/main.go:100`, `:107-112`;
`pkg/fitpolicy/report.go:29`).

- `--rounds 1` is the unchanged legacy path: the single sample decides, no round bookkeeping is
  attached to the row, and the report claims `rounds: 1` with `cases: "1/1"`
  (`scripts/fit-probe/rounds.go:11-15`, `:49-50`, `scripts/fit-probe/report.go:158-164`).
- `--rounds N>1` decides a row only on a **strict majority** of the requested rounds: more than half
  must agree on consistent, or on divergence (`scripts/fit-probe/rounds.go:53-71`). A tie or a
  plurality leaves the row inconclusive, and an inconclusive row is omitted from the payload and
  named on stderr with its per-round verdicts (`scripts/fit-probe/rounds.go:92-96`, `:173-181`);
  the test that pins this is `TestR15SplitVoteIsUndecidedAndOmitted`
  (`scripts/fit-probe/regression_test.go:1844`).
- **Every requested round counts in the denominator**, including a round that was inconclusive or
  could not execute, so an abstention makes a majority harder, never easier; a 2-round run needs
  both rounds to agree (`scripts/fit-probe/rounds.go:19-23`).
- The encoded `cases` is `<votes>/<rounds>` (`scripts/fit-probe/report.go:158-164`); a row that
  claims several rounds without a vote count is omitted rather than written with `0/N`
  (`scripts/fit-probe/report.go:155-164`).
- A majority row carries the **weakest** evidence tier among the rounds that voted for it; if any
  deciding round has no known tier, the tier is left empty and the row is omitted by the encoder
  (`scripts/fit-probe/rounds.go:124-139`, `:100-102`, `scripts/fit-probe/report.go:204-213`).
  Repetition cannot launder an acceptance-tier vote past `--acceptance-marks`
  (`scripts/fit-probe/rounds.go:24-27`).
- Rounds are independent, strictly sequential, and re-send exactly the same marshalled bytes
  (`scripts/fit-probe/main.go:396-404`, `:405-444`, `:578-583`). Each round **re-measures the
  official baseline** when one is configured, so a multi-round run costs N times the single-round
  request count, relay and official alike (`scripts/fit-probe/main.go:396-403`,
  `scripts/fit-probe/rounds.go:28-30`).

### 2.4 `--payload-in FILE`: what it validates and refuses

`--payload-in` lets the operator post a curated row set through the same single write path instead
of the run's own payload (`scripts/fit-probe/payload_in.go:3-31`, `scripts/fit-probe/main.go:469-492`).
The file contributes the row set and the `supported` flags; **everything else is regenerated by the
run**: the envelope (`report_id`, `run_id`, `suite`, `generated_at`), the policy binding, `rounds`,
each row's `cases`, and `expires_at` (`scripts/fit-probe/payload_in.go:90-94`, `:147`;
`scripts/fit-probe/report.go:230-240`; the encoder never sets an explicit expiry, so the posted rows
carry `expires_at: 0`, `pkg/fitpolicy/report.go:53-55`). The file needs no envelope of its own and a
stale one cannot leak into the POST (`scripts/fit-probe/payload_in.go:67-70`).

It refuses, naming the offending row, when:

- the JSON shape is not a suite report, contains an unknown field (a typo such as `"suported": true`
  would otherwise be read as the opposite claim), or has trailing data
  (`scripts/fit-probe/payload_in.go:79-89`);
- a row is outside this run's probed scope, by the normalized
  `(channel_id, family, model, behavior)` key (`scripts/fit-probe/payload_in.go:54-65`, `:119-123`);
- a row repeats an earlier row (`scripts/fit-probe/payload_in.go:124-126`);
- this run did not decide the row — an undecided row "cannot be corroborated" and aborts the write
  (`scripts/fit-probe/payload_in.go:129-143`);
- the file's `supported` contradicts this run's own decision, majority included
  (`scripts/fit-probe/payload_in.go:131-135`; tests `TestR17...`,
  `scripts/fit-probe/regression_test.go:1971`);
- the run's own evidence tier for the row is not encodable under the current flags, so a curated row
  cannot smuggle acceptance-tier evidence past the gate (`scripts/fit-probe/payload_in.go:136-139`);
- fewer rows survive this run's encoder than the file carried (`scripts/fit-probe/payload_in.go:151-153`).

Every refusal happens **before** the POST, so a rejected payload costs no write
(`scripts/fit-probe/payload_in.go:105-109`; `TestR16...`, `scripts/fit-probe/regression_test.go:1916`).
Rows this run decided but the curated subset leaves out are **not written**: they keep their stored
value (`scripts/fit-probe/payload_in.go:49-51`).

### 2.5 `--acceptance-marks`: off by default, and why that is the false-positive direction

By default the encoder writes **structural-strength evidence only**
(`scripts/fit-probe/main.go:99`, `scripts/fit-probe/report.go:204-213`). With the flag, a bare
accepted response (`2xx`, parseable JSON, content or finish_reason) is enough to write
`supported=true` for the acceptance-only marks — `image.parts` and `family.whole`
(`scripts/fit-probe/probe.go:791-794`, `scripts/fit-probe/spec.go:74-85`, `:156-168`). For
`family.whole` that one accepted response is the entire evidence for pinning a whole family
(`pkg/fitpolicy/builtin.go:69-77`).

The flag is not symmetric insurance: it opens **both** directions at the weakest tier. The
acceptance-tier `prompt_tokens` difference is a divergence signal, and it too is gated behind the
flag so that it is "not written as `supported=false` unless `--acceptance-marks` says
acceptance-level evidence may decide" (`scripts/fit-probe/report.go:178-186`;
`scripts/fit-probe/probe.go:725-739` returns the mark's own tier, not a hard-coded structural one).
So the flag lowers the bar for writing marks, in both directions; it never adds evidence. The
recorded glm-5.3 run is the reference case: 11 of 14 `family.whole` rows were acceptance-consistent
in at least two rounds and were omitted because the mandated run did not pass the flag
(`~/workos/scratch/fit-capability-run5-report.md` §4, §6).

### 2.6 Serial execution, one retry

There is no concurrency in the program sources: rounds and targets are plain sequential loops
(`scripts/fit-probe/main.go:396-404`, `:405-444`), and no `go func`/`sync`/`WaitGroup` construct
appears in `main.go`, `probe.go`, `client.go`, `report.go`, `rounds.go`, `payload_in.go` or
`spec.go` (the test file uses `sync.Mutex` only to guard counters in its local `httptest` handlers).

Each probe is one HTTP request, plus **at most one retry**, and only on a transport error or a
retryable status (`scripts/fit-probe/main.go:789-809`). Retryable means exactly `429, 500, 502, 503,
504` (`scripts/fit-probe/main.go:833-840`). Deterministic 4xx answers are never retried
(`scripts/fit-probe/main.go:802-805`). A retry stays inside its round and never becomes an extra
round (`scripts/fit-probe/main.go:398-403`). The official baseline request has **no** retry at all
(`scripts/fit-probe/main.go:811-831`).

### 2.7 Credentials and what leaves the process

Credentials are read once from the environment variables named by `--admin-token-env`
(default `FIT_PROBE_ADMIN_TOKEN`) and `--relay-token-env` (default `FIT_PROBE_RELAY_TOKEN`)
(`scripts/fit-probe/main.go:46-49`, `:83-84`, `:246-247`). They are never printed: the report carries
only the booleans `admin_token_present` / `relay_token_present`
(`scripts/fit-probe/main.go:194-197`, `:530-533`). The relay probe is pinned to one channel by
appending `-<channelID>` to the relay key (`scripts/fit-probe/client.go:179-192`), which requires an
admin-owned token and short-circuits the fit policy; a relay key that itself contains a hyphen would
break that pin, so keys are used hyphen-free (the recorded runs shape-check the key as 48
alphanumeric characters with no hyphen — `~/workos/scratch/fit-capability-run4-report.md` §5).
Response bodies are never retained or printed — only
the derived `signature` fields leave the process (`scripts/fit-probe/probe.go:280-316`); free text
that does leave is redacted and bounded (`scripts/fit-probe/report.go:22-59`), base URLs are reduced
to `scheme://host`, redirects are refused (`scripts/fit-probe/client.go:78-80`) and response reads
are capped at 512 KiB (`scripts/fit-probe/client.go:109-111`). The tool writes nothing to disk by
itself (`scripts/fit-probe/main.go:27-29`); stdout/stderr are the only sinks.

---

## 3. Recommended usage

### 3.0 Build and pin first

```bash
cd /home/vhbs/tokeness/apps/new-api
go build -o ~/workos/scratch/fit-probe-verify ./scripts/fit-probe
md5sum ~/workos/scratch/fit-probe-verify   # record it and re-assert it before each round
```

Go does not embed constant names, so a hash pin plus the runtime assertions in the plan are the
stale-binary guard (§7). Credentials come from the environment; nothing below writes a credential
anywhere.

### 3.1 (a) A read-only plan

Reads the admin channel list, sends **no probe and no write**, and prints one `planned_probes` row
per target with the mark's own `max_tokens` and the round count
(`scripts/fit-probe/main.go:96`, `:315-335`, `:341-362`). `--explain` is required — without it the
tool prints only the payload, which in plan mode has no results.
`--dry-run=false` is required: with the dry-run default you would get the offline placeholder plan.

```bash
FIT_PROBE_ADMIN_TOKEN=... \
  ~/workos/scratch/fit-probe-verify --dry-run=false --plan --explain \
  --base-url https://<gateway-host> \
  --channel 4,5,9 --family kimi-k3 --behaviors tools.choice_semantics,response_format.json \
  > ~/workos/scratch/fit-probe-plan.json
```

No relay token is needed for a plan. `--plan` costs a paged `GET /api/channel/` only
(`scripts/fit-probe/client.go:120-154`); it does **not** read the live policy, so the planned mark
list is the shipped default document (`scripts/fit-probe/main.go:587-589`). If the live policy
requires a mark the default does not, the real run will probe more than the plan shows — check
`--explain`'s `mark_mapping` and the live policy before treating the plan as complete.

For a zero-network plan of the shipped default policy (channel id 0, first official model name per
family), use `--offline --explain` instead (`scripts/fit-probe/main.go:711-729`).

### 3.2 (b) A conservative measurement with no write

Doc-only (no official endpoint), three rounds, structural-strength marks only, no `--write`:

```bash
FIT_PROBE_ADMIN_TOKEN=... FIT_PROBE_RELAY_TOKEN=... \
  ~/workos/scratch/fit-probe-verify --dry-run=false --rounds 3 --explain \
  --base-url https://<gateway-host> \
  --channel 4,5,9 \
  > ~/workos/scratch/fit-probe-measure.json \
  2> ~/workos/scratch/fit-probe-measure.err
```

This sends probes but changes nothing outside the process: `--write` is the only write path, and
`--acceptance-marks`/`--official-base-url` are deliberately absent, so acceptance-tier and
baseline-blocked rows stay unwritten (they are still visible in `results`). Keep the stderr file:
that is where the `fit-probe: undecided ...` lines for majority ties go
(`scripts/fit-probe/rounds.go:173-181`).

### 3.3 (c) A conservative 3-round write

The same run plus the single write flag:

```bash
FIT_PROBE_ADMIN_TOKEN=... FIT_PROBE_RELAY_TOKEN=... \
  ~/workos/scratch/fit-probe-verify --dry-run=false --rounds 3 --write --explain \
  --base-url https://<gateway-host> \
  --channel 4,5,9 \
  > ~/workos/scratch/fit-probe-write.json \
  2> ~/workos/scratch/fit-probe-write.err
```

Only strict-majority, structural-strength rows are posted, each with `cases: "<votes>/3"`, bound to
the live policy read at the start of the run (`scripts/fit-probe/report.go:226-240`;
`scripts/fit-probe/main.go:586-609`). To restrict the written set to named rows, add
`--payload-in ~/workos/scratch/curated.json`; every curated row must be inside this run's probed
scope and agree with this run's own decision (§2.4). A report whose rows are all omitted is refused
before the POST (`scripts/fit-probe/main.go:459-461`).

### 3.4 The cost model, and how to estimate before spending

Per round, for each planned target `(channel, model, mark)`:

- **1 relay HTTP request** to `/v1/chat/completions`, plus at most 1 retry, so **1–2 requests**
  (`scripts/fit-probe/client.go:186-192`, `scripts/fit-probe/main.go:791-809`);
- **+1 official request** for each distinct `(family, model, mark)` *only if* an official baseline is
  configured and its key env var is non-empty; that request reuses the identical bytes and has no
  retry (`scripts/fit-probe/main.go:405-431`, `:811-831`);
- **output tokens ≤ that mark's cap** (§4): 8 for `logprobs.dual_path`, `image.parts` and
  `usage.thinking_counting`; 96 for `tools.choice_semantics`; 16 for `response_format.json`; 1 for
  `history.assistant_first`, `tools.dynamic_names` and `family.whole`
  (`scripts/fit-probe/probe.go:114-272`). Input tokens are not bounded by the tool; the recorded
  round-2 measurement was ~72 prompt tokens per probe (141 probes ≈ 10.2k prompt + 3.3k completion
  ≈ 13.5k tokens, `~/workos/scratch/fit-capability-run2-report.md:131`), and that completion figure
  predates the raised logprobs/tools caps, so do not use it as a completion-side upper bound.

To compute an estimate before spending anything:

1. build and hash the binary;
2. run the read-only plan of §3.1 and count `planned_probes` (call it `R`; each row already carries
   `max_tokens` and, for `--rounds>1`, `rounds`);
3. relay requests ≈ `R × rounds` (worst case `× 2` for retries); distinct official requests per
   round = number of distinct `(family, model, behavior)` rows in the plan, and only when a baseline
   is configured; worst-case completion tokens ≈ `Σ max_tokens × rounds`;
4. after the run, `--explain`'s `summary.probe_requests` gives the relay requests actually sent
   (retries counted), while `summary.omitted_results`/`undecided` say how many rows did not become
   marks (`scripts/fit-probe/main.go:206-222`, `:516-521`, `:886-905`). `probe_requests` deliberately
   excludes the official-baseline requests (`scripts/fit-probe/main.go:215-218`).

Scope narrows with `--channel`, `--model`, `--family`, `--behaviors` and `--channel-status`
(`scripts/fit-probe/main.go:85-88`, `:103`, `:676-731`). A requested mark the policy cannot require
is ignored with `fit-probe: ignoring unknown mark ...` on stderr
(`scripts/fit-probe/main.go:633-653`).

---

## 4. Probe budgets, and why each value is what it is

One probe = one generation with a hard `max_tokens` cap. The cap is both the wire value and the
classifier's threshold, so a cap that is too small silently converts "the channel lacks the
capability" into "the probe ran out of room". That happened twice, and both caps were raised with
the evidence recorded in the source. The two raised budgets are named constants used by both the
spec and the body; the other six repeat the literal in both places.

| mark | cap | where | why this value |
|---|---|---|---|
| `logprobs.dual_path` | **8** | `scripts/fit-probe/probe.go:96`, used at `:118` (spec) and `:121` (body) | **Raised 1 → 8.** At a 1-token cap every accepting channel answers `finish_reason=length` at `completion_tokens=1`, so the cut is an artefact of the probe; 11 of the 15 stranded `supported=false` marks were undecidable for exactly that reason. 8 is the smallest budget in the table that lets a one-word answer (`hi`) finish naturally, and it is the cap `usage.thinking_counting` already used, so the worst-case cost of this probe moved from 1 to 8 completion tokens and nothing else changed (`scripts/fit-probe/probe.go:87-96`; `~/workos/scratch/fit-capability-run4-report.md` §1.A). |
| `tools.choice_semantics` | **96** | `scripts/fit-probe/probe.go:110`, used at `:189` and `:192` | **Raised 16 → 96.** At 16 tokens the eight kimi-k3 rows that produced no `tool_calls` all ended exactly 16/16 with empty content, while the three channels that did answer `tool_choice=required` needed 60–76 completion tokens: the budget cut the answer off before the tool call could be emitted. 96 is the smallest round budget with headroom over the observed 76; the follow-up run observed real tool calls up to exactly 96/96, so the headroom was necessary (`scripts/fit-probe/probe.go:98-110`; `~/workos/scratch/fit-capability-run5-report.md` §1.A, §3). |
| `usage.thinking_counting` | **8** | `scripts/fit-probe/probe.go:151` (spec) and `:154` (body) | Pre-existing budget, not raised in these runs; it is the reference the logprobs raise adopted as "the smallest budget in this table that lets a one-word answer finish naturally" (`scripts/fit-probe/probe.go:92-95`). The deepseek-v4 evidence (`reasoning_content` or a reported reasoning-token field) is generated output, and the truncation guard tolerates a spent budget for that class (`scripts/fit-probe/probe.go:660-669`). |
| `image.parts` | **8** | `scripts/fit-probe/probe.go:138` (spec) and `:145` (body) | Same 8-token budget as `usage.thinking_counting`; the prompt asks for a single word (`scripts/fit-probe/probe.go:142`). The source records no separate rationale or history for this pre-existing 8 — **not verified** beyond "it is one of the two 8s that existed before the cap raises" (`~/workos/scratch/fit-capability-run4-report.md` §2 lists it as unchanged). |
| `response_format.json` | **16** | `scripts/fit-probe/probe.go:215` (spec) and `:221` (body) | Pre-existing and deliberately **not** raised (`~/workos/scratch/fit-capability-run5-report.md` §1.B). This is the remaining cap that still costs rows: 7 of 11 kimi-k3 rows were undecided because those channels spent the 16-token budget on reasoning and returned no content (`~/workos/scratch/fit-capability-run5-report.md` §4, §6). It stays safe rather than wrong because absence is not divergence for this mark — `StructuralAbsenceIsDivergence` is unset and the comparison against a baseline only fires for a baseline that itself produced JSON (`scripts/fit-probe/probe.go:225-234`), so a truncation here yields inconclusive, never a false mark. |
| `history.assistant_first` | **1** | `scripts/fit-probe/probe.go:239` (spec) and `:246` (body) | The compared quantity is the request's `prompt_tokens` against a measured baseline, and the response reports `usage` regardless of how much output was generated; acceptance needs only content or a finish reason, either of which a 1-token answer provides (`scripts/fit-probe/probe.go:335-342`, `:367`, `scripts/fit-probe/spec.go:133-143`). The source records no other rationale for 1 — **not verified** beyond that. |
| `tools.dynamic_names` | **1** | `scripts/fit-probe/probe.go:252` (spec) and `:261` (body) | Same as above: `prompt_tokens`-only discriminator, so the generation budget does not matter (`scripts/fit-probe/spec.go:144-155`). |
| `family.whole` | **1** | `scripts/fit-probe/probe.go:267` (spec) and `:269` (body) | Acceptance-only mark: one accepted response is the entire structural claim, so one token is enough (`scripts/fit-probe/spec.go:156-168`). |

**Truncation is handled fail-closed, not by a bigger cap alone.** `looksTruncated` can only demote an
absence-derived divergence to inconclusive, never promote anything, and it decides differently for
the two evidence classes: for generated-output marks either `finish_reason=length` or "no content
and `completion_tokens >= max_tokens`" is a truncation; for envelope marks (logprobs) a length cut
counts only when it emitted no token at all, because a field attached per emitted token is not
explained by the cut (`scripts/fit-probe/probe.go:638-669`, reached only from the absence branch at
`scripts/fit-probe/probe.go:761-781`). The history of the rule, including the pre-fix failing test
output, is in `~/workos/scratch/fit-capability-run3-report.md` §1 and
`~/workos/scratch/fit-capability-run4-report.md` §1.B.

---

### 4.1 The deepseek-v4 logprobs probe pins thinking off (R20)

`logprobs.dual_path` used to leave the thinking state to the model default, and DeepSeek V4 defaults to
thinking-on: at this probe's 8-token budget the model usually spends the whole budget on reasoning, and the
official endpoint attaches logprobs to the tokens it *does* emit, so whether the reference carried logprobs was
a coin flip rather than a fact about the channel. Measured on byte-identical requests (2026-09-30, official
ch5): the official reference returned logprobs in 1 of 3 rounds, and in one round the official *baseline* leg
said `logprobs=true` while the identically pinned official *sample* leg said `false` — ch5 diverging from ch5.
Every presence-derived deepseek row therefore rested on a reference that could not be reproduced.

The probe now pins `thinking.type=disabled` for the deepseek-v4 family — the mechanism the kimi probes already
use (`scripts/fit-probe/probe.go:141`; kimi, unchanged, at `:180`) — so the answer is content, which is the path
the official endpoint attaches logprobs to. Nothing else moved: budget 8, the `logprobs`/`top_logprobs` fields,
and the `logprobs=true` shape the policy pins. Verified read-only, 3 rounds, official baseline = relay ch5
(`~/workos/scratch/fit-official-r20/verify.json`): the official reference signature was **identical in all
three rounds for all three models** — `status=200, accepted, prompt_tokens=5, has_logprobs=true,
has_content=true, completion_tokens=8, reasoning_tokens absent` — where the pre-fix probe had
`prompt_tokens=31/84, has_logprobs=false, has_reasoning_content=true` in two of three rounds. New regression
test: `TestR20DeepSeekProbesPinTheThinkingState` (`scripts/fit-probe/regression_test.go`).

**`usage.thinking_counting` (deepseek) is deliberately NOT switched to thinking-off.** Its policy rule fires
for thinking-*expected* requests (`pkg/fitpolicy/builtin.go:39`) and its evidence is reasoning output, so the
thinking-off shape would (a) probe a shape the policy never consults for this mark and (b) compare
`reasoning_token_accounting` against an official reference that omits `completion_tokens_details` for
disabled-thinking requests by contract (`relay/channel/openai/deepseek_v4_fit.go:129-130`) — every channel that
reports `reasoning_tokens` (0 included) would be written `supported=false` for a property of the probe, which
is the "invented false" this tool exists to refuse. Its reference therefore stays non-reproducible: in the same
verification run the official leg flipped to a direct content answer (no `reasoning_content`,
`reasoning_tokens=0`) in round 2 of `deepseek-v4.1-flash`, and a pre-fix run recorded the same flip on
`deepseek-v4-flash`. Rows whose deciding field is that presence comparison stay unwritten until the official
endpoint's own thinking-expected sampling is reproducible.

**Citation shift:** this revision adds 19 lines to `probe.go` inside the logprobs spec, so every
`probe.go:NNN` citation elsewhere in this file that points past line 120 is 19 lines lower in this copy; §1.2
has already been renumbered. `main.go`, `report.go`, `rounds.go`, `payload_in.go`, `client.go` and `spec.go`
are unchanged, so their citations are unaffected.

## 5. What cannot be measured without an official baseline

Four `(family, mark)` rows are structurally undecidable in a doc-only run:

| family | mark | discriminator (source) | why a lone probe cannot decide it |
|---|---|---|---|
| kimi-k3 | `usage.thinking_counting` | `prompt_tokens` only (`scripts/fit-probe/spec.go:98-108`) | the mark is about the official endpoint's prompt-token accounting (the documented +67-token delta); comparing a count needs a reference count, and the probe has none |
| kimi-k3 | `history.assistant_first` | `prompt_tokens` only (`scripts/fit-probe/spec.go:133-143`) | same: the assistant-first history is claimed to change the prompt accounting, and only a measured reference can show it |
| kimi-k3 | `tools.dynamic_names` | `prompt_tokens` only (`scripts/fit-probe/spec.go:144-155`) | same |
| glm-5.3 | `family.whole` | `acceptance` only (`scripts/fit-probe/spec.go:156-168`, basis kind `mechanism`, `officialfit/officialfit.go:100`) | a bare accepted response is the whole evidence; without `--acceptance-marks` the row cannot be encoded, and with it the claim is acceptance-level only |

The tool **will not invent a verdict for these**. With no measured baseline, a `prompt_tokens`-only
mark returns inconclusive with the basis "no measured official baseline: this dimension's
discriminator is prompt_tokens, which a lone probe cannot check"
(`scripts/fit-probe/probe.go:782-784`), and a mark with no expectation-table row at all returns
inconclusive rather than treating a 200 OK as support (`scripts/fit-probe/probe.go:785-790`). The
recorded run confirms it: all 11 rows of each kimi-k3 `prompt_tokens`-only mark came back
inconclusive in all three rounds and nothing was written, while the acceptance-only glm rows were
omitted because the required flag was not passed
(`~/workos/scratch/fit-capability-run5-report.md` §4, §6).

What configuring an official baseline changes:

- `--official-base-url` (repeatable, bare or `family=URL`) and `--official-key-env` (repeatable, bare
  or `family=ENV`) point the tool at a real official endpoint per family
  (`scripts/fit-probe/main.go:89-90`, `:131-143`). The **identical marshalled body** is sent to the
  relay and to the official endpoint, so the comparison is between two identical requests
  (`scripts/fit-probe/main.go:411-427`, `scripts/fit-probe/client.go:194-206`).
- A baseline that the official endpoint itself rejects or does not accept is unusable and the row
  stays inconclusive (`scripts/fit-probe/probe.go:686-690`).
- With an accepted baseline, the mark's structural comparison runs, and the `prompt_tokens`
  discriminator is checked: equal counts on a `prompt_tokens`-only mark ⇒ consistent, a difference ⇒
  divergence — both at that mark's own tier, so both land behind `--acceptance-marks`
  (`scripts/fit-probe/probe.go:720-742`, `scripts/fit-probe/report.go:178-186`).
- The baseline is re-measured every round, so it is part of the per-round cost
  (`scripts/fit-probe/main.go:396-403`, `scripts/fit-probe/rounds.go:28-30`).
- `--explain` reports what was actually used: `baseline.source` is `docs` for the table or `measured`
  for a live baseline, with the redacted official URL and the measured families
  (`scripts/fit-probe/main.go:502-514`).

Two failure modes to know before trusting a "measured" run:

1. **Doubled path.** `officialPath` appends `/v1/chat/completions` unless the base already ends in
   `/chat/completions`, so a base of `https://host/v1` produces `https://host/v1/v1/chat/completions`
   (`scripts/fit-probe/client.go:200-206`). The 404 is not accepted, and every mark of that family
   becomes inconclusive with no louder warning than `baseline.source` in `--explain`.
2. **Silent doc-only downgrade.** A family whose base URL or key env var is missing or empty simply
   gets no baseline (`scripts/fit-probe/main.go:811-820`); the run reverts to doc-only semantics for
   that family with no error, visible only as `baseline.source: "docs"`.

For glm-5.3 `family.whole`, a measured baseline buys nothing that `--acceptance-marks` does not
already unlock: the mark has no `StructuralCompare`, so a matching baseline still returns
consistent at acceptance strength (`scripts/fit-probe/probe.go:720-743`), and both the doc-only and
the baselined paths stay behind the flag (`scripts/fit-probe/report.go:204-213`).

---

## 6. Known limits and their rules

### 6.1 Structurally unattributable prose-only errors (R4)

A channel rejection is a divergence **only** when it is attributable to this dimension: either the
error object structurally names one of the probe's own fields, or a measured official baseline
accepted the identical request (`scripts/fit-probe/probe.go:693-716`). "Structurally" is narrow by
design: only the error object's own locator values (`error.param`/`error.parameter`/`error.field`,
matched as whole identifier tokens) and category values (`error.code`/`error.type`, matched exactly)
count. The prose message is never consulted — `annotateErrorField` says so explicitly: a generic
rejection that merely mentions a field word ("content policy", "tools unavailable", "invalid usage")
"is not evidence that the channel rejected that parameter, and treating it as evidence writes a
structural `supported=false` derived from a coincidence"
(`scripts/fit-probe/probe.go:411-428`, `:429-460`). An unattributed rejection stays inconclusive and
therefore writes nothing (`scripts/fit-probe/probe.go:715`).

The real case: three deepseek-v4 `logprobs.dual_path` rows (ch 4 pro, ch 4 v4.1-flash, ch 19 pro)
answer HTTP 400 with prose that literally names the field — "The parameters `logprobs` is not
supported." — but carry no `error.param`/`error.field`. The prose strongly suggests
`supported=false`; the tool refuses to assert it, and the rule was deliberately **not** widened
(`~/workos/scratch/fit-capability-run3-report.md` §5;
`~/workos/scratch/fit-capability-run4-report.md` §7, §8;
`~/workos/scratch/fit-capability-run5-report.md` §4). Do not widen it to "fix" a row: the moment
prose counts, every incidental mention of `content`/`tools`/`usage` becomes a false structural
`false`. Report the row as unattributable, or get a measured baseline that accepts the same request
(which makes the rejection decidable without reading prose).

### 6.2 Single-sample instability, and why `--rounds > 1`

A single sample is not a decision. Two independent runs of the tool disagreed on **25 of 141 rows
(18%)**, including a `logprobs.dual_path` row that one run stored as `supported=false` and the next
measured `true` (`scripts/fit-probe/rounds.go:5-9`;
`~/workos/scratch/fit-capability-run2-report.md` §6). That is why `--rounds N` exists and why a
strict majority was chosen over plurality: ties and abstentions leave the row undecided rather than
written (§2.3). The three-round runs did not reproduce 18% on the narrower scopes they measured
(zero mixed votes across 43 stored rows in run 3, `~/workos/scratch/fit-capability-run3-report.md`
§3), but they did show individual rows flipping between rounds — e.g. ch 5 flash
consistent/divergence/divergence and ch 28 pro divergence/divergence/consistent, both settled
`false` by 2-of-3 (`~/workos/scratch/fit-capability-run4-report.md` §4), and ch 46 response_format
JSON in only 1 of 3 rounds, left undecided (`~/workos/scratch/fit-capability-run5-report.md` §4).
One stored boolean cannot express "3 of 4 samples returned the field"
(`~/workos/scratch/fit-capability-run4-report.md` §8).

### 6.3 Output-token comparison without pinned sampling

The probe pins **no** sampling parameter — `chatBody` carries only `model`, `messages`, `max_tokens`
and `stream:false` (`scripts/fit-probe/probe.go:63-70`) — and it cannot pin one for kimi-k3, which
accepts one fixed temperature per thinking state (`scripts/fit-probe/probe.go:172-179`, citing
`relay/helper/valid_request.go:1061-1062`). Therefore the tool does **not** compare exact reasoning
token counts or completion counts as evidence: two identical requests may legitimately report
different counts, and an exact-count mismatch would write `supported=false` for sampling variance.
Only the reproducible accounting facts are compared — whether reasoning content is present, and
whether a reasoning-token field was reported at all (`scripts/fit-probe/probe.go:172-182`). The one
output-token count comparison left is the truncation predicate, and it is one-directional: it can
only turn an absence into inconclusive, never into a verdict
(`scripts/fit-probe/probe.go:657-669`).

### 6.4 Supersede exists; retraction does not

A later report **supersedes** the rows it carries: the endpoint applies them and bumps each row's
revision (recorded per row across `~/workos/scratch/fit-capability-run3-report.md` §6,
`fit-capability-run4-report.md` §6 and `fit-capability-run5-report.md` §5).

There is **no retraction path**. The only write in the program is the suite-report POST
(`scripts/fit-probe/client.go:208-211`); nothing in the package issues a DELETE, and a stored row is
a plain boolean plus a `cases` string with no "unknown" value to write
(`pkg/fitpolicy/report.go:52-63`). A report applies only the rows it carries, so a row left out
persists untouched with its old value — which `--payload-in` states explicitly
(`scripts/fit-probe/payload_in.go:49-51`), and which the round-2 audit recorded from the panel side
as "no retraction path ... a report applies only the rows it carries, so absent rows persist
untouched" (`~/workos/scratch/fit-capability-run2-report.md` §9).

What an operator therefore **can** do about a stored mark that later looks wrong:

- re-measure it (≥3 rounds, same scope) and write the value the strict majority justifies; the write
  supersedes the old value and is recorded as a new revision
  (`scripts/fit-probe/report.go:226-240`, and the revision tables cited above);
- leave it alone and document a caveat, which is what the recorded runs did for the three
  prose-only-400 rows (`~/workos/scratch/fit-capability-run4-report.md` §7, §8;
  `fit-capability-run5-report.md` §4, §6);
- narrow a write to specific rows with `--payload-in`, knowing that omitted rows keep their value.

What an operator **cannot** do with this tool: delete a mark, reset it, mark it unknown, or write a
non-boolean. Writing a resolved-looking value the run did not measure is also blocked: `--payload-in`
refuses an undecided row and refuses a `supported` flag that contradicts this run's own decision
(`scripts/fit-probe/payload_in.go:129-143`), and a report with no decisive rows is refused before the
POST (`scripts/fit-probe/main.go:459-461`).

---

## 7. Operator mistakes we already made

1. **A stale binary from `/tmp` cost a whole run.** The first post-fix re-run silently reused the
   previous round's `/tmp/fit-probe` (built before the guard existed; the verification step had
   compiled to `/dev/null` instead of a real path). The symptom was the new guard's fingerprint count
   coming back 0 with the old rows unchanged; it was confirmed by hashing against a fresh build, and
   the wasted run cost **141 probes ≈ 13k tokens**
   (`~/workos/scratch/fit-capability-run2-report.md` §2). The rule that came out of it, and that
   every later round followed: build fresh to a named path, record the md5, re-assert it before every
   round, and assert a marker in the binary's own output — Go does not embed const names, so a hash
   plus a runtime assertion on the live plan is the guard
   (`~/workos/scratch/fit-capability-run3-report.md` §2,
   `fit-capability-run4-report.md` §3, `fit-capability-run5-report.md` §2).
2. **Trusting a single sample.** The 18% cross-run disagreement, including a stored `false` that a
   later sample measured `true`, is what forced the three-round majority rule, and the `cases` field
   is what now records the tally (`scripts/fit-probe/rounds.go:5-9`,
   `scripts/fit-probe/report.go:158-164`;
   `~/workos/scratch/fit-capability-run2-report.md` §6). Rule: never write a first-sample verdict for
   a row you care about; use `--rounds 3` and let abstentions and ties stay unwritten.
3. **Widening an attribution rule instead of reporting the row as unattributable.** The prose-only
   400s name `logprobs` in English but carry no structural locator. The tempting fix is to grep the
   message; that was rejected, the rule was left narrow, and the three rows were reported as
   unasserted rather than written (`~/workos/scratch/fit-capability-run3-report.md` §5,
   `fit-capability-run4-report.md` §7, `fit-capability-run5-report.md` §4). Rule: an unattributable
   rejection is a finding to report, not a verdict to extract.

---

## 8. Provenance of the claims in this file, and what is not verified

Evidence base: this working copy of `scripts/fit-probe/` (hashes at the top) plus the scratch audit
reports named inline. The four reports the operator's brief named are
`~/workos/scratch/fit-probe-matrix.md` (a read-only audit of an **earlier** revision),
`fit-capability-run3-report.md`, `fit-capability-run4-report.md` and `fit-capability-run5-report.md`.
Two adjacent scratch reports were used for facts not present in those four:
`fit-capability-run2-report.md` (the stale-binary incident, §2; the 18% instability, §6; the absence
of a retraction path, §9) and `fit-capability-run-report.md` (the round-1 request accounting).

The code has moved past several report findings; where the matrix and the current source disagree,
the source is authoritative:

- The matrix's "≤16 completion tokens" ceiling and its cap table (1/8/16) are obsolete: the logprobs
  cap is now 8 (`scripts/fit-probe/probe.go:96`) and the tools cap is now 96
  (`scripts/fit-probe/probe.go:110`).
- Matrix R1 (kimi thinking diverges on every accepted response), R2 (baseline cached per
  `(family, model)`), R3 (prompt_tokens mismatch hard-coded to structural strength), R4 (substring
  attribution over the prose fingerprint), R6 (deny-list strength gate; `hasStructuralEvidence`
  defaulting to `Accepted`), R8 (dead code), the exact-count half of R7 and the logprobs-`{}` /
  JSON-scalar halves of R5 are **fixed** in the current source: the absence branch requires
  structural strength and a known evidence rule (`scripts/fit-probe/probe.go:756-781`), the baseline
  key is the full request signature (`scripts/fit-probe/main.go:744-746`), the token-count difference
  carries the mark's own tier (`scripts/fit-probe/probe.go:725-739`), attribution is structural
  (`scripts/fit-probe/probe.go:411-460`), the tier gate is an allow-list
  (`scripts/fit-probe/report.go:199-213`), `structuralEvidence` returns `(known, present)` with
  `false` as the default (`scripts/fit-probe/probe.go:804-817`), an expectation-less mark is
  inconclusive (`scripts/fit-probe/probe.go:785-790`), reasoning counts are no longer compared
  exactly (`scripts/fit-probe/probe.go:172-182`), logprobs evidence now requires a non-empty token
  carrier (`scripts/fit-probe/probe.go:535-550`), `response_format.json` requires a JSON *object*
  (`scripts/fit-probe/probe.go:493-533`), and the dead helpers the matrix listed are gone from the
  current files.
- Still true from the matrix: R5's "`reasoning_tokens: 0` counts as accounting evidence"
  (`scripts/fit-probe/probe.go:813` uses `>= 0`), R9's doubled official path
  (`scripts/fit-probe/client.go:200-206`) and R10's silent doc-only downgrade
  (`scripts/fit-probe/main.go:811-820`).
- Run 3's §6 statement that the tool "has no payload-input mode" is obsolete: `--payload-in` now
  exists and is validated through the same write path (`scripts/fit-probe/main.go:101`,
  `scripts/fit-probe/payload_in.go`), with regression tests `TestR16`–`TestR19`
  (`scripts/fit-probe/regression_test.go:1916`, `:1971`, `:2216`, `:2279`).
- The reports' file hashes for `main.go`, `report.go` and `regression_test.go` predate the current
  copy, which added `--rounds`/`--payload-in` and the R12–R19 tests plus the new
  `rounds.go`/`payload_in.go`. Use the pin at the top of this file, not the reports' hashes.

Explicitly **not verified**:

- why `image.parts` and `usage.thinking_counting` are 8 and `response_format.json` is 16 — the
  source records the rationale only for the two raised caps; the other values are pre-existing and
  the reports list them as unchanged, not as justified;
- that the shipped default policy equals the live policy on any particular deployment — the tool
  reads the live document on a real run (`scripts/fit-probe/main.go:586-609`), so check
  `GET /api/fit-policy` before assuming a plan's mark list is complete;
- any runtime claim for the *earlier* revisions: the recorded `go build`/`gofmt`/`go vet`/`go test`
  results are from the run-3/4/5 revisions. On the R20 revision (`go1.25.1`) all four were re-run
  clean on 2026-09-30: `go build ./scripts/fit-probe`, `gofmt -l scripts/fit-probe/` (empty),
  `go vet ./scripts/fit-probe/`, `go test ./scripts/fit-probe/` = ok with **26** top-level test
  functions passing (25 in `regression_test.go`, 1 in `report_test.go`), and `go test -race` = ok.
  `TestR12SingleRoundIsUnchangedAndIsTheDefault` (`scripts/fit-probe/regression_test.go:1575`) and
  `TestR13ThreeRoundMajorityIsEncoded` (`:1654`) for the rounds contract.
