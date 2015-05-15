// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"time"
)

// TODO(rsc): Add -tbr, along with standard exceptions (doc/go1.5.txt)

func cmdSubmit(args []string) {
	flags.Usage = func() {
		fmt.Fprintf(stderr(), "Usage: %s submit %s [commit-hash]\n", os.Args[0], globalFlags)
	}
	flags.Parse(args)
	if n := len(flags.Args()); n > 1 {
		flags.Usage()
		os.Exit(2)
	}

	b := CurrentBranch()
	var c *Commit
	if len(flags.Args()) == 1 {
		c = b.CommitByHash("submit", flags.Arg(0))
	} else {
		c = b.DefaultCommit("submit")
	}

	// No staged changes.
	// Also, no unstaged changes, at least for now.
	// This makes sure the sync at the end will work well.
	// We can relax this later if there is a good reason.
	checkStaged("submit")
	checkUnstaged("submit")

	// Fetch Gerrit information about this change.
	g, err := b.GerritChange(c, "LABELS", "CURRENT_REVISION")
	if err != nil {
		dief("%v", err)
	}

	// Check Gerrit change status.
	switch g.Status {
	default:
		dief("cannot submit: unexpected Gerrit change status %q", g.Status)

	case "NEW", "SUBMITTED":
		// Not yet "MERGED", so try the submit.
		// "SUBMITTED" is a weird state. It means that Submit has been clicked once,
		// but it hasn't happened yet, usually because of a merge failure.
		// The user may have done git sync and may now have a mergable
		// copy waiting to be uploaded, so continue on as if it were "NEW".

	case "MERGED":
		// Can happen if moving between different clients.
		dief("cannot submit: change already submitted, run 'git sync'")

	case "ABANDONED":
		dief("cannot submit: change abandoned")
	}

	// Check for label approvals (like CodeReview+2).
	// The final submit will check these too, but it is better to fail now.
	for _, name := range g.LabelNames() {
		label := g.Labels[name]
		if label.Optional {
			continue
		}
		if label.Rejected != nil {
			dief("cannot submit: change has %s rejection", name)
		}
		if label.Approved == nil {
			dief("cannot submit: change missing %s approval", name)
		}
	}

	// Upload most recent revision if not already on server.

	if c.Hash != g.CurrentRevision {
		run("git", "push", "-q", "origin", b.PushSpec(c))

		// Refetch change information, especially mergeable.
		g, err = b.GerritChange(c, "LABELS", "CURRENT_REVISION")
		if err != nil {
			dief("%v", err)
		}
	}

	// Don't bother if the server can't merge the changes.
	if !g.Mergeable {
		// Server cannot merge; explicit sync is needed.
		dief("cannot submit: conflicting changes submitted, run 'git sync'")
	}

	if *noRun {
		dief("stopped before submit")
	}

	// Otherwise, try the submit. Sends back updated GerritChange,
	// but we need extended information and the reply is in the
	// "SUBMITTED" state anyway, so ignore the GerritChange
	// in the response and fetch a new one below.
	if err := gerritAPI("/a/changes/"+fullChangeID(b, c)+"/submit", []byte(`{"wait_for_merge": true}`), nil); err != nil {
		dief("cannot submit: %v", err)
	}

	// It is common to get back "SUBMITTED" for a split second after the
	// request is made. That indicates that the change has been queued for submit,
	// but the first merge (the one wait_for_merge waited for)
	// failed, possibly due to a spurious condition. We see this often, and the
	// status usually changes to MERGED shortly thereafter.
	// Wait a little while to see if we can get to a different state.
	const steps = 6
	const max = 2 * time.Second
	for i := 0; i < steps; i++ {
		time.Sleep(max * (1 << uint(i+1)) / (1 << steps))
		g, err = b.GerritChange(c, "LABELS", "CURRENT_REVISION")
		if err != nil {
			dief("waiting for merge: %v", err)
		}
		if g.Status != "SUBMITTED" {
			break
		}
	}

	switch g.Status {
	default:
		dief("submit error: unexpected post-submit Gerrit change status %q", g.Status)

	case "MERGED":
		// good

	case "SUBMITTED":
		// see above
		dief("cannot submit: timed out waiting for change to be submitted by Gerrit")
	}

	// Sync client to revision that Gerrit committed, but only if we can do it cleanly.
	// Otherwise require user to run 'git sync' themselves (if they care).
	run("git", "fetch", "-q")
	if len(b.Pending()) == 1 {
		if err := runErr("git", "checkout", "-q", "-B", b.Name, g.CurrentRevision, "--"); err != nil {
			dief("submit succeeded, but cannot sync local branch\n"+
				"\trun 'git sync' to sync, or\n"+
				"\trun 'git branch -D %s; git change master; git sync' to discard local branch", b.Name)
		}
	} else {
		printf("submit succeeded; run 'git sync' to sync")
	}

	// Done! Change is submitted, branch is up to date, ready for new work.
}
