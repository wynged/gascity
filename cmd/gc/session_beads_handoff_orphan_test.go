package main

import (
	"bytes"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// A polecat that pushes its branch but dies before completing the refinery
// handoff can leave its work bead with gc.routed_to cleared. When the
// reconciler reaps the dead session bead, releaseWorkFromClosedSessionBead
// clears the assignee and reopens the work — but if it does not also restore a
// route, the bead is stranded open+unassigned+unrouted: invisible to BOTH the
// pool demand probe (which keys on gc.routed_to) and releaseOrphanedPoolAssignments
// (which skips empty-routed beads). The fix passes the owning pool route,
// recovered from the closing session's own template metadata, as ReleaseWorkBead's
// run_target fallback; restoreCarriedWorkRoutes (#3421) then backfills gc.routed_to
// from that run_target so a fresh worker re-claims it.
func TestReleaseWorkFromClosedSessionBeadRestoresPoolRouteForUnroutedWork(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "gastown__polecat-th-87n",
			"template":     "gascity/gastown.polecat",
			"state":        "active",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	// Completed-and-pushed work whose routing was lost mid-handoff: a branch
	// on origin, no gc.routed_to, no gc.run_target, assigned to the dead session.
	work, err := store.Create(beads.Bead{
		Title:    "handoff-orphan work",
		Status:   "open",
		Assignee: sessionBead.ID,
		Metadata: map[string]string{"branch": "polecat/ga-n2d.2"},
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	inProgress := "in_progress"
	if err := store.Update(work.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("mark work in_progress: %v", err)
	}

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want empty", got.Assignee)
	}
	if got.Status != "open" {
		t.Fatalf("status = %q, want open", got.Status)
	}
	if got.Metadata[beadmeta.RunTargetMetadataKey] != "gascity/gastown.polecat" {
		t.Fatalf("gc.run_target = %q, want gascity/gastown.polecat (restored pool route; restoreCarriedWorkRoutes re-stamps gc.routed_to from it so the pool demand probe re-discovers the work)", got.Metadata[beadmeta.RunTargetMetadataKey])
	}
	if got.Metadata[beadmeta.RoutedToMetadataKey] != "" {
		t.Fatalf("gc.routed_to = %q, want empty immediately after release (restoreCarriedWorkRoutes backfills it from gc.run_target on the next tick)", got.Metadata[beadmeta.RoutedToMetadataKey])
	}
}

// Workflow-kind beads recover their route via the same ReleaseWorkBead
// run_target fallback as plain work; they re-claim through the legacy
// gc.run_target queue (see #2860), which restoreCarriedWorkRoutes (#3421)
// recognizes for pre-eld2x workflow roots.
func TestReleaseWorkFromClosedSessionBeadRestoresRunTargetForWorkflowKind(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "worker-1",
			"template":     "graph/worker",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	work, err := store.Create(beads.Bead{
		Title:    "graph step",
		Status:   "in_progress",
		Assignee: sessionBead.ID,
		Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if got.Metadata[beadmeta.RunTargetMetadataKey] != "graph/worker" {
		t.Fatalf("gc.run_target = %q, want graph/worker (workflow-kind work routes via gc.run_target)", got.Metadata[beadmeta.RunTargetMetadataKey])
	}
	if got.Metadata[beadmeta.RoutedToMetadataKey] != "" {
		t.Fatalf("gc.routed_to = %q, want empty for workflow-kind work", got.Metadata[beadmeta.RoutedToMetadataKey])
	}
}

// Work that still carries a route must be left untouched — only truly unrouted
// orphans get a restored route. Guards against clobbering an in-flight route.
func TestReleaseWorkFromClosedSessionBeadLeavesExistingRouteUntouched(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "worker-1",
			"template":     "gascity/gastown.polecat",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	work, err := store.Create(beads.Bead{
		Title:    "still-routed work",
		Status:   "in_progress",
		Assignee: sessionBead.ID,
		Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "gascity/other-pool"},
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want empty", got.Assignee)
	}
	if got.Metadata[beadmeta.RoutedToMetadataKey] != "gascity/other-pool" {
		t.Fatalf("gc.routed_to = %q, want unchanged gascity/other-pool", got.Metadata[beadmeta.RoutedToMetadataKey])
	}
}

// When the closing session bead carries no template/agent_name, there is no
// route to recover — the work is still released, just without a restored route.
func TestReleaseWorkFromClosedSessionBeadWithoutTemplateStillReleases(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:    "worker",
		Type:     sessionBeadType,
		Labels:   []string{sessionBeadLabel},
		Metadata: map[string]string{"session_name": "worker-1"},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	work, err := store.Create(beads.Bead{
		Title:    "unrouted work, unknown pool",
		Status:   "in_progress",
		Assignee: sessionBead.ID,
		Metadata: map[string]string{"branch": "polecat/ga-n2d.2"},
	})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}

	var stderr bytes.Buffer
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work bead: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want empty", got.Assignee)
	}
	if got.Status != "open" {
		t.Fatalf("status = %q, want open", got.Status)
	}
	if got.Metadata[beadmeta.RoutedToMetadataKey] != "" {
		t.Fatalf("gc.routed_to = %q, want empty (no template to recover a route from)", got.Metadata[beadmeta.RoutedToMetadataKey])
	}
}

// A crew handoff note is mail addressed to the very session that is cycling
// out, so a session-reap sweep finds it on an assignee-keyed query. If the
// sweep treats it as work it "releases" it — clearing the assignee that IS the
// delivery address — and the successor's inbox no longer matches it: the note
// is silently lost while gc handoff reports success (ch-l0f1v). Mail must
// survive every reap path intact.
//
// Mail beads are ephemeral, so only the TierBoth sweeps (named-session
// retirement, orphan-pool release) actually reach them; the closed-session
// release runs the unflagged issues-tier query. Both are driven here so the
// exemption holds if either query widens.
func TestSessionReapLeavesHandoffMailDeliverable(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:  "dorito",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":              "dorito",
			"configured_named_identity": "pringle/dorito",
			"template":                  "pringle/crew",
			"state":                     "active",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	// The outgoing handoff note, addressed the way gc handoff addresses it:
	// to the session's own address, so the successor picks it up on wake.
	mailbox := beadmail.New(store)
	sent, err := mailbox.SendHandoff(mail.HandoffIntent{
		From:     "pringle/dorito",
		To:       "pringle/dorito",
		Subject:  "HANDOFF: Session cycling",
		Body:     "branch fix/x is pushed; PR not opened yet",
		ThreadID: "thread-handoff",
	})
	if err != nil {
		t.Fatalf("send handoff mail: %v", err)
	}

	// The session cycles: the reconciler retires the named session and then
	// releases whatever the closed session bead still held.
	var stderr bytes.Buffer
	unclaimWorkAssignedToRetiredSessionBead(store, nil, sessionBead, "pringle/crew", &stderr)
	releaseWorkFromClosedSessionBead(store, sessionBead, &stderr)

	got, err := store.Get(sent.ID)
	if err != nil {
		t.Fatalf("get handoff mail bead: %v", err)
	}
	if got.Assignee != "pringle/dorito" {
		t.Fatalf("handoff mail assignee = %q, want pringle/dorito preserved — the assignee IS the delivery address, so clearing it orphans the note", got.Assignee)
	}

	// The end-to-end symptom: the successor runs `gc mail inbox` and must see it.
	inbox, err := mailbox.Inbox("pringle/dorito")
	if err != nil {
		t.Fatalf("read successor inbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ID != sent.ID {
		t.Fatalf("successor inbox = %+v, want the handoff note %s (the fresh session came up blind)", inbox, sent.ID)
	}
}

// The gate side of the same rule. An unread message must not count as work
// holding the session open: with mail's assignee now preserved across the reap,
// a message that counted as work would keep the dead session's bead from ever
// closing, and an on-demand crew session would never respawn.
func TestHasNonSessionWorkIgnoresMail(t *testing.T) {
	store := beads.NewMemStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	msg, err := beadmail.New(store).Send("pringle/utz", "pringle/dorito", "question", "body")
	if err != nil {
		t.Fatalf("send mail: %v", err)
	}
	msgBead, err := store.Get(msg.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if wa.HasNonSessionWork([]beads.Bead{msgBead}) {
		t.Fatal("HasNonSessionWork = true for mail alone, want false — an unread note is not work and must not hold the session bead open")
	}

	work, err := store.Create(beads.Bead{Title: "real work", Status: "open", Assignee: "pringle/dorito"})
	if err != nil {
		t.Fatalf("create work bead: %v", err)
	}
	if !wa.HasNonSessionWork([]beads.Bead{msgBead, work}) {
		t.Fatal("HasNonSessionWork = false with a real work bead present, want true")
	}
}

// Reassignment is the other mutator keyed by session identity: re-homing a
// retired session's work onto its successor. Mail must not travel that way
// either — the inbox matches on the address, so re-pointing a note at a
// successor's bead ID would take it out of every inbox.
func TestReassignWorkAssignedToRetiredSessionBeadLeavesMailAddressed(t *testing.T) {
	store := beads.NewMemStore()

	sessionBead, err := store.Create(beads.Bead{
		Title:  "dorito",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":              "dorito",
			"configured_named_identity": "pringle/dorito",
			"template":                  "pringle/crew",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	sent, err := beadmail.New(store).Send("mayor", "pringle/dorito", "review this", "body")
	if err != nil {
		t.Fatalf("send mail: %v", err)
	}

	var stderr bytes.Buffer
	reassignWorkAssignedToRetiredSessionBead(store, nil, sessionBead, "ch-successor", &stderr)

	got, err := store.Get(sent.ID)
	if err != nil {
		t.Fatalf("get mail bead: %v", err)
	}
	if got.Assignee != "pringle/dorito" {
		t.Fatalf("mail assignee = %q, want pringle/dorito — mail is addressed, not re-homed", got.Assignee)
	}
}
