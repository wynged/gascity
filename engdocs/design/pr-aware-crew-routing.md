---
title: "PR-Aware Crew Routing via Assignee Affinity"
---

| Field | Value |
|---|---|
| Status | Proposed |
| Date | 2026-04-07 |
| Author(s) | — |
| Issue | — |
| Supersedes | — |

## Summary

A workflow design for project-oriented agent orchestration where crew
pool members develop affinity to specific work (PRs, branches, tasks)
and are intelligently reused when follow-up work arrives. Uses
assignee-based routing — already implemented in Gas City — combined
with an external event ingestion pipeline to stamp incoming work with
the right assignee. No custom `work_query` or `scale_check` required.

## Problem

A developer works with a crew member on a rig. They collaborate on a
feature branch, open a PR, and move on to other work. Later, PR review
comments arrive. The developer wants the system to:

1. **Reuse the original crew member** if it's still alive or asleep on
   that branch — it has context, the worktree, the conversation history.
2. **Wake it up** if it went to sleep after idle.
3. **Spawn a fresh crew member** if the original is gone (drained,
   closed, or expired).

The orientation is around **the work** (the PR, the project, the
branch), not around named agents. Crew members are fungible resources
that develop temporary affinity to work items.

## Design

### Config: Crew Pool

```toml
[[agent]]
name = "crew"
scope = "rig"
sleep_after_idle = "10m"
wake_mode = "resume"            # preserve conversation across wake cycles

[agent.pool]
min = 0
max = 5
# Default scale_check counts routed beads — no override needed
```

Key settings:
- **`sleep_after_idle = "10m"`** — crew members sleep after 10 minutes
  of idle, freeing resources but preserving their session bead (and
  thus their branch/worktree state).
- **`wake_mode = "resume"`** — when a sleeping crew member wakes, it
  resumes its conversation. The agent retains context about what it
  was working on.
- **`min = 0`** — no crew members alive when there's no work.
- **`max = 5`** — hard cap on concurrent crew members per rig.

### How It Works: The Three-Tier Work Query

Gas City's default `work_query` (defined in
`internal/config/config.go:1327`) is a three-tier shell script that
already implements the affinity logic. No custom override is needed.

**Tier 1: Crash recovery** (in_progress + assigned to me)

```sh
for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do
  [ -z "$id" ] && continue
  r=$(bd list --status in_progress --assignee="$id" --json --limit=1 2>/dev/null)
  [ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0
done
```

If this crew member was mid-task when it crashed or was killed, it
rediscovers that work first. The bead's assignee matches one of the
session's three identity strings.

**Tier 2: Pre-assigned work** (ready + assigned to me)

```sh
for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do
  [ -z "$id" ] && continue
  r=$(bd ready --assignee="$id" --json --limit=1 2>/dev/null)
  [ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0
done
```

This is the key tier for PR-aware routing. When a PR comment bead is
created with `assignee=<session-name-of-original-crew>`, that specific
crew member sees it here. Other crew members skip it (their identity
doesn't match).

**Tier 3: Pool queue** (ready + unassigned + routed to template)

```sh
bd ready --metadata-field gc.routed_to=<rig>/crew \
  --unassigned --json --limit=1 2>/dev/null
```

Fallback for work that has no specific assignee. Any available crew
member picks it up. This handles the case where the original crew
member is gone.

### How Sessions Wake: ComputeAwakeSet

The reconciler's `ComputeAwakeSet` function
(`cmd/gc/compute_awake_set.go:88`) decides which sessions should be
awake on every tick. The relevant wake paths for this workflow:

**Assigned-work wake** (lines 218-241):

```go
for _, bead := range input.SessionBeads {
    if bead.State == "closed" || bead.Drained {
        continue
    }
    for _, wb := range input.WorkBeads {
        if assignee == bead.ID || assignee == bead.SessionName ||
            assignee == bead.NamedIdentity || assignee == bead.Template {
            desired[bead.SessionName] = "assigned-work"
            break
        }
    }
}
```

This matches work bead assignees against four session identifiers:
- `bead.ID` — the session bead's unique store ID
- `bead.SessionName` — the tmux session name (e.g., `mycity--myrig--crew-3`)
- `bead.NamedIdentity` — canonical name for named sessions
- `bead.Template` — the agent template (e.g., `myrig/crew`)

When a PR-comment bead has `assignee=mycity--myrig--crew-3`, this
loop finds crew-3's session bead (even if asleep) and marks it
`"assigned-work"` — waking that specific instance.

**Scale-check wake** (lines 149-178):

If no assignee matches (original crew member is gone), the bead is
still routed to `myrig/crew` via `gc.routed_to` metadata. The default
`scale_check` counts these beads, and the pool scales up a fresh
instance.

**Idle-sleep suppression** (lines 288-305):

```go
if decision.ShouldWake && !input.AttachedSessions[name] && !bead.IdleSince.IsZero() {
    if hasAgent && agent.SleepAfterIdle > 0 {
        idleTimeout = agent.SleepAfterIdle
    }
    if idleTimeout > 0 && input.Now.Sub(bead.IdleSince) >= idleTimeout {
        decision.ShouldWake = false
        decision.Reason = "idle-sleep"
    }
}
```

A crew member with no assigned work and idle past `sleep_after_idle`
transitions to sleep. Its session bead persists (state=asleep), so it
can be woken later by assigned-work demand.

### Scenario Matrix

| Scenario | What happens |
|---|---|
| Crew-3 is asleep, has the branch | Assigned-work wake fires → crew-3 wakes → tier 2 finds the bead → resumes on same branch with conversation context |
| Crew-3 is live, working on that PR | Already awake → tier 1 or tier 2 finds the new bead on next hook check → model decides priority |
| Crew-3 is live, working on something else | Stays awake (assigned-work) → sees new assigned work on next hook → GUPP applies: model decides what to do |
| Crew-3 is gone (drained/closed) | Session bead is closed → assignee doesn't match any living session → scale_check counts routed bead → pool spawns fresh crew member → tier 3 picks up work |
| No crew members exist at all | scale_check sees routed bead → pool creates crew-1 → tier 3 picks it up |
| Crew-3 is asleep, PR bead has no assignee | scale_check sees 1 bead → wakes one session (may or may not be crew-3) → tier 3 picks it up |

### The Pipeline: External Events → Beads with Assignees

The only piece that must be built is the ingestion pipeline that
creates beads when external events arrive (PR comments, CI failures,
etc.) and stamps them with the correct assignee.

**Step 1: When a crew member opens a PR**, record the association.

The crew member's prompt template should instruct it to record
metadata when it opens a PR:

```
When you open a pull request, record the association:
  bd update <your-work-bead> --set-metadata gc.pr=<pr-number>
  bd update <your-work-bead> --set-metadata gc.branch=<branch-name>
```

This creates a lookup record: PR #42 → crew-3's work bead → crew-3
as assignee.

**Step 2: When a PR event arrives**, look up the original crew member
and create a new bead.

```bash
#!/bin/bash
# pr-event-handler.sh — called by webhook listener or polling script
PR_NUMBER="$1"
EVENT_TYPE="$2"  # "review_comment", "ci_failure", etc.
BODY="$3"

# Look up who originally worked on this PR
ORIGINAL_BEAD=$(bd list --metadata-field gc.pr="$PR_NUMBER" \
  --json 2>/dev/null | jq -r '.[0] // empty')
ORIGINAL_ASSIGNEE=$(echo "$ORIGINAL_BEAD" | jq -r '.assignee // empty')

# Create a new work bead for the event
BEAD_ID=$(bd create --json \
  --title "PR #${PR_NUMBER}: ${EVENT_TYPE}" \
  --metadata gc.pr="$PR_NUMBER" \
  --metadata gc.event_type="$EVENT_TYPE" \
  | jq -r '.id')

# Route to the crew template (so scale_check sees it)
bd update "$BEAD_ID" --set-metadata gc.routed_to=myrig/crew

# If we know who handled it, assign directly for affinity
if [ -n "$ORIGINAL_ASSIGNEE" ]; then
  bd update "$BEAD_ID" --assignee="$ORIGINAL_ASSIGNEE"
fi
```

**Assignee resolution order:**
1. If the original work bead has an assignee (session name) → use it
   for direct affinity routing.
2. If no assignee is found → bead is routed but unassigned → pool
   treats it as new work and any available crew member picks it up.

**Step 3: The reconciler tick fires.**

- `buildDesiredState` collects assigned work beads.
- `ComputeAwakeSet` sees assigned work for crew-3 → marks it
  `"assigned-work"`.
- If crew-3 is asleep → wake it.
- If crew-3 is alive → it stays alive.
- If crew-3 is gone → no match → scale_check handles it.

**Step 4: The crew member runs `gc hook`.**

- Tier 1: any in-progress work? (crash recovery)
- Tier 2: any ready work assigned to me? → **finds the PR bead**
- Tier 3: any unassigned routed work? (fallback)

The crew member picks up the bead and handles the review comment.
It's still on the same branch in the same worktree with conversation
context from `wake_mode=resume`.

## Infrastructure Dependencies

This workflow depends on the following implemented Gas City features:

| Feature | Location | Role |
|---|---|---|
| Three-tier work query | `internal/config/config.go:1327-1351` | Assignee-first work discovery with pool fallback |
| Assigned-work wake | `cmd/gc/compute_awake_set.go:218-241` | Wakes specific sleeping sessions when assigned work arrives |
| Pool scale_check | `internal/config/config.go:1383-1393` | Counts routed beads to determine pool size |
| sleep_after_idle | `cmd/gc/compute_awake_set.go:288-305` | Idle crew members sleep instead of staying warm |
| wake_mode=resume | `internal/config/config.go:1296-1305` | Preserves conversation context across sleep/wake |
| Pool instances | `cmd/gc/pool.go` | Numbered crew members (crew-1, crew-2, ...) |
| Session bead persistence | `cmd/gc/build_desired_state.go` | Sleeping sessions retain their bead (identity, metadata) |
| GUPP principle | Prompt templates | Agent decides priority when multiple beads are assigned |
| gc.routed_to metadata | `internal/config/config.go:1360-1365` | Template-level routing for pool queue |
| Idle-sleep lifecycle | `cmd/gc/session_reconcile.go` | Drain → asleep with sleep_reason, wake on demand |

## What Must Be Built

| Component | Complexity | Description |
|---|---|---|
| PR metadata convention | Trivial | Prompt template tells crew to `bd update` with `gc.pr` and `gc.branch` metadata when opening PRs |
| Event ingestion script | Moderate | Webhook listener or polling script that creates beads from GitHub events and stamps assignees |
| Webhook infrastructure | Moderate | GitHub webhook endpoint or polling cron that feeds events to the ingestion script |

None of these require changes to Gas City's Go code. The entire
workflow is composed from existing primitives: bead metadata, assignee
fields, pool scaling, and the three-tier work query.

## Design Decisions

### Why assignee routing, not custom work_query

A custom `work_query` with branch-awareness was considered (Option B).
Problems:

1. **Controller-side probe runs without session context.** The
   reconciler calls `work_query` for demand detection with empty
   identity env vars — there's no git worktree to check against.
2. **work_query runs in the rig directory**, not the agent's worktree.
   `git rev-parse --abbrev-ref HEAD` returns the rig's branch, not
   the per-agent branch.
3. **Race condition.** Multiple crew members could match the same
   branch-affinity bead and waste wake cycles.
4. **Requires paired scale_check override.** Custom routing logic
   must be consistent across both commands.

Assignee routing avoids all of these: the assignee is a discrete
string that matches exactly one session, and the default three-tier
query already handles it.

### Why pools, not named agents per project

Named agents (one per project) would work but create management
overhead. With pools:

- Crew members are **fungible by default**, developing **temporary
  affinity** via assignee metadata.
- No config changes needed when projects start or end.
- Pool scaling handles resource management automatically.
- The developer focuses on work items, not agent lifecycle.

### Why sleep_after_idle, not idle_timeout

`idle_timeout` **kills** the session process and immediately re-wakes
it if still desired — it's a stale-session restart mechanism, not a
resource-saving mechanism. `sleep_after_idle` truly puts the session
to sleep: process stops, session bead persists with
`state=asleep`, and it only wakes when real demand arrives (assigned
work, scale_check, attachment).

## Future Extensions

- **Automatic PR metadata**: A bd hook (`on_create`) could detect PR
  creation patterns and auto-stamp `gc.pr` metadata without prompt
  instructions.
- **Branch-aware sling**: A `gc sling` wrapper that looks up PR→branch
  associations and sets assignees automatically.
- **PR dashboard**: `bd list --metadata-field gc.pr=42` shows all
  work beads for a PR — a project-oriented view of agent activity.
- **Stale assignee cleanup**: A periodic script that clears assignees
  pointing to closed session beads, allowing pool fallback.
