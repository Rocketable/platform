---------------------------- MODULE DraftLifecycle ----------------------------
(* Save path of one shared Agent Editor draft for one agent file.
   Fixes selects plan changes; Fixes = {} is the plan as written. *)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Users, Mode, HasGh, MergeRule, Outcomes, Kinds, Fixes,
          MaxCrash, MaxFail, MaxThird

AllFixes == {"cas", "prevalidate", "atomicwrite", "targetbase", "cmpwrite",
             "cmpwrite_nolock", "guardundo", "reloadfirst", "snapcheck", "startreset", "stopwait"}

ASSUME Mode \in {"ws", "overlay"}
ASSUME HasGh \in BOOLEAN /\ MergeRule \in {"contains", "contains_or_bytes"}
ASSUME Outcomes \subseteq {"merge", "squash", "rebase", "close", "none"} /\ Outcomes # {}
ASSUME Kinds \subseteq {"push", "pr"} /\ Kinds # {}
ASSUME Fixes \subseteq AllFixes
ASSUME {MaxCrash, MaxFail, MaxThird} \subseteq Nat

\* File contents: v0 = bytes when the draft began, t = a third party's edit,
\* d/e = draft contents (e = after a co-editor's later edit), torn = half-written file.
Vers == {"v0", "t", "d", "e", "torn"}

(* --algorithm DraftLifecycle {
variables
  valid \in {f \in [Vers -> BOOLEAN] : f["v0"] /\ f["t"] /\ ~f["torn"]},
  disk = "v0",          \* workspace agents/<name>.md (Mode ws)
  branch = "v0",        \* agents/<name>.md at the overlay's pinned branch head (Mode overlay)
  runtimeFile = "v0",   \* copy the running server loaded (runtime dir), changes only on Reload/start
  up = TRUE, epoch = 0, crashes = 0, fails = 0, thirdWrites = 0, broken = FALSE,
  status = "editing", content = "d", base = "v0", saved = "none", liveBytes = "none",
  kind = "none", prState = "none", fate = "none", contains = FALSE,
  ep = [u \in Users |-> 0], snap = [u \in Users |-> "none"],
  reviewed = [u \in Users |-> "none"], prev = [u \in Users |-> "none"],
  stageHead = [u \in Users |-> "none"],
  badOverwrite = FALSE, lostEdit = FALSE, falseLive = FALSE;

define {
  Fix(f) == f \in Fixes
  Source == IF Mode = "ws" THEN disk ELSE branch
  StaleTarget == IF Fix("targetbase") THEN disk ELSE runtimeFile
  OkToOverwrite(u) == disk \in {base, reviewed[u]}
  Merged == IF HasGh /\ kind = "pr" THEN prState \in {"merge", "squash", "rebase"}
            ELSE contains \/ (MergeRule = "contains_or_bytes" /\ branch = saved)
  ClosedSeen == HasGh /\ kind = "pr" /\ prState = "closed"
  SaveSteps == {"Check", "Recheck", "Write", "Write2", "Reload", "Undo", "Stage", "Publish"}
  AtRest == up /\ \A u \in Users : pc[u] = "Idle" \/ ep[u] # epoch

  TypeOK == disk \in Vers /\ branch \in Vers /\ runtimeFile \in Vers
            /\ status \in {"editing", "saving", "waiting", "live"}
  \* 1. R18/R19 save safety: at rest the file the next start loads is valid; start never breaks.
  NoInvalidLive == AtRest => valid[Source]
  StartupOK == ~broken
  \* 2. R21: a save only replaces bytes equal to the draft's base or to what the saver reviewed.
  StaleNeedsReview == ~badOverwrite
  \* 3. R16/R17: one live save per draft; "live" means the draft's contents are what went live.
  SaveExclusive == Cardinality({u \in Users : pc[u] \in SaveSteps /\ ep[u] = epoch}) <= 1
  NoMixedLive == status = "live" => content = liveBytes
  \* 5. discovered
  HonestLive == ~falseLive
  NoUndoClobber == ~lostEdit
  UndoUnreachable == \A u \in Users : pc[u] # "Undo"
  \* 4. R19 liveness
  WaitingResolves == (status = "waiting") ~> (status \in {"live", "editing"})
  SavingResolves == (status = "saving") ~> (status # "saving")
  \* vacuity probes: each must be VIOLATED (the state is reachable)
  ProbeNotLive == status # "live"
  ProbeNotWaiting == status # "waiting"
  ProbeNoCrashSave == ~(~up /\ \E u \in Users : pc[u] \in SaveSteps)
  ProbeNoPrLive == ~(status = "live" /\ kind = "pr")
  ProbeNoReloadFailUndo == ~(\E u \in Users : pc[u] = "Undo" /\ valid[disk])
  ProbeNoStaleCatch == ~(\E u \in Users : reviewed[u] = "t" /\ pc[u] \in {"Write", "Publish"})
  ProbeNoInvalidDraft == ~(\E u \in Users : pc[u] \in SaveSteps /\ ~valid[snap[u]])
}

macro MarkLive() {
  if (Fix("snapcheck") /\ (content # saved \/ runtimeFile # saved)) {
    status := "editing"; base := saved;
  } else {
    status := "live"; liveBytes := saved;
    if (runtimeFile # saved) falseLive := TRUE;
  };
}

\* end of a save request: drop its scratch values (keeps idle states canonical)
macro Done() {
  ep[self] := 0; snap[self] := "none"; reviewed[self] := "none";
  prev[self] := "none"; stageHead[self] := "none";
}

fair process (user \in Users) {
Idle:
  either {
    \* Save button
    await up /\ status = "editing";
    ep[self] := epoch;
    if (Fix("cas")) {
      status := "saving"; snap[self] := content;
      if (Mode = "ws") goto Check; else goto Stage;
    } else goto Click2;
  } or {
    \* a co-editor changes the draft (once)
    await content = "d" /\ status \in {"editing", "saving", "waiting"};
    content := "e"; goto Idle;
  } or {
    \* Stop waiting / back to editing (closes the PR when gh is available)
    await Fix("stopwait") /\ up /\ status = "waiting";
    status := "editing"; prState := "none"; goto Idle;
  };
Click2:
  \* plan: status was read in Idle; it is set here in a separate write (no CAS)
  if (ep[self] # epoch) { Done(); goto Idle; }
  else {
    status := "saving"; snap[self] := content;
    if (Mode = "ws") goto Check; else goto Stage;
  };
Check:
  \* R21 staleness check (+ review), then optional loader validation of the draft alone
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Fix("prevalidate") /\ ~valid[snap[self]]) { status := "editing"; Done(); goto Idle; }
  else {
    either {
      \* reloadfirst: Reload's own validation runs on a stage holding the draft, before any write
      await Fix("reloadfirst") /\ fails < MaxFail;
      fails := fails + 1; status := "editing"; Done(); goto Idle;
    } or {
      if (StaleTarget # base) reviewed[self] := StaleTarget;
      if (Fix("cmpwrite_nolock")) goto Recheck; else goto Write;
    };
  };
Recheck:
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (~OkToOverwrite(self)) { status := "editing"; Done(); goto Idle; }
  else goto Write;
Write:
  \* plan: os.Root WriteFile (truncate, then fill); atomicwrite: temp file + rename
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Fix("cmpwrite") /\ ~OkToOverwrite(self)) { status := "editing"; Done(); goto Idle; }
  else {
    if (~OkToOverwrite(self)) badOverwrite := TRUE;
    prev[self] := disk; saved := snap[self];
    if (Fix("atomicwrite")) { disk := snap[self]; goto Reload; }
    else { disk := "torn"; goto Write2; };
  };
Write2:
  if (ep[self] # epoch) { Done(); goto Idle; }
  else { disk := snap[self]; goto Reload; };
Reload:
  \* validated Reload: fails on an invalid file, or (bounded) for reasons outside this file
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (valid[disk] /\ fails < MaxFail /\ ~Fix("reloadfirst")) {
    either { runtimeFile := disk; MarkLive(); Done(); goto Idle; }
    or { fails := fails + 1; goto Undo; };
  } else if (valid[disk]) { runtimeFile := disk; MarkLive(); Done(); goto Idle; }
  else goto Undo;
Undo:
  \* plan: put the old bytes back
  if (ep[self] # epoch) { Done(); goto Idle; }
  else {
    if (Fix("guardundo")) { if (disk = snap[self]) disk := prev[self]; }
    else { if (disk # snap[self]) lostEdit := TRUE; disk := prev[self]; };
    status := "editing"; Done(); goto Idle;
  };
Stage:
  \* clone pinned branch, staleness check (+ review), write, validate with the loader, commit
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (~valid[snap[self]]) { status := "editing"; Done(); goto Idle; }
  else {
    stageHead[self] := branch;
    with (cur = IF Fix("targetbase") THEN branch ELSE runtimeFile) {
      if (cur # base) reviewed[self] := cur;
    };
    with (k \in Kinds) { kind := k; };
    goto Publish;
  };
Publish:
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (kind = "push") {
    \* git refuses a non-fast-forward push (approximated by comparing file contents)
    if (branch # stageHead[self]) { status := "editing"; Done(); goto Idle; }
    else {
      if (stageHead[self] \notin {base, reviewed[self]}) badOverwrite := TRUE;
      saved := snap[self]; branch := snap[self];
      contains := TRUE; prState := "none"; status := "waiting"; Done(); goto Idle;
    };
  } else {
    saved := snap[self]; prState := "open"; contains := FALSE; status := "waiting";
    with (o \in Outcomes) { fate := o; };
    Done(); goto Idle;
  };
}

\* GitHub: eventually applies the PR's fate, picked from Outcomes when the PR opened ("none" = never acts)
fair process (gh = "gh") {
GhAct:
  while (TRUE) {
    await prState = "open" /\ fate # "none";
    if (fate = "close") prState := "closed";
    else {
      prState := fate; branch := saved;
      contains := (fate = "merge");
    };
  };
}

\* Server poller: gh pr view with gh; otherwise "pinned branch contains our commit" (or bytes rule)
fair process (poller = "poller") {
Poll:
  while (TRUE) {
    await up /\ status = "waiting" /\ (Merged \/ ClosedSeen);
    if (ClosedSeen) status := "editing";
    else if (fails < MaxFail) {
      either { runtimeFile := branch; MarkLive(); }
      or { fails := fails + 1; };
    } else { runtimeFile := branch; MarkLive(); };
  };
}

\* main agent / teammate: edits the live file at any time, or triggers a Reload
process (third = "third") {
Third:
  while (TRUE) {
    either {
      await thirdWrites < MaxThird;
      thirdWrites := thirdWrites + 1;
      if (Mode = "ws") disk := "t";
      else branch := "t";
    } or {
      await up;
      if (Mode = "ws") { if (valid[disk]) runtimeFile := disk; }
      else runtimeFile := branch;
    };
  };
}

process (crasher = "crasher") {
Crash:
  while (TRUE) {
    await up /\ crashes < MaxCrash;
    up := FALSE; epoch := epoch + 1; crashes := crashes + 1;
  };
}

\* start loads whatever is on disk (overlays are re-fetched at the pinned ref)
fair process (restarter = "restarter") {
Restart:
  while (TRUE) {
    await ~up;
    up := TRUE;
    if (Mode = "ws") { if (valid[disk]) runtimeFile := disk; else broken := TRUE; }
    else runtimeFile := branch;
    if (Fix("startreset") /\ status = "saving") status := "editing";
  };
}
} *)
\* BEGIN TRANSLATION (chksum(pcal) = "2b3d8621" /\ chksum(tla) = "83fbd15a")
VARIABLES valid, disk, branch, runtimeFile, up, epoch, crashes, fails, 
          thirdWrites, broken, status, content, base, saved, liveBytes, kind, 
          prState, fate, contains, ep, snap, reviewed, prev, stageHead, 
          badOverwrite, lostEdit, falseLive, pc

(* define statement *)
Fix(f) == f \in Fixes
Source == IF Mode = "ws" THEN disk ELSE branch
StaleTarget == IF Fix("targetbase") THEN disk ELSE runtimeFile
OkToOverwrite(u) == disk \in {base, reviewed[u]}
Merged == IF HasGh /\ kind = "pr" THEN prState \in {"merge", "squash", "rebase"}
          ELSE contains \/ (MergeRule = "contains_or_bytes" /\ branch = saved)
ClosedSeen == HasGh /\ kind = "pr" /\ prState = "closed"
SaveSteps == {"Check", "Recheck", "Write", "Write2", "Reload", "Undo", "Stage", "Publish"}
AtRest == up /\ \A u \in Users : pc[u] = "Idle" \/ ep[u] # epoch

TypeOK == disk \in Vers /\ branch \in Vers /\ runtimeFile \in Vers
          /\ status \in {"editing", "saving", "waiting", "live"}

NoInvalidLive == AtRest => valid[Source]
StartupOK == ~broken

StaleNeedsReview == ~badOverwrite

SaveExclusive == Cardinality({u \in Users : pc[u] \in SaveSteps /\ ep[u] = epoch}) <= 1
NoMixedLive == status = "live" => content = liveBytes

HonestLive == ~falseLive
NoUndoClobber == ~lostEdit
UndoUnreachable == \A u \in Users : pc[u] # "Undo"

WaitingResolves == (status = "waiting") ~> (status \in {"live", "editing"})
SavingResolves == (status = "saving") ~> (status # "saving")

ProbeNotLive == status # "live"
ProbeNotWaiting == status # "waiting"
ProbeNoCrashSave == ~(~up /\ \E u \in Users : pc[u] \in SaveSteps)
ProbeNoPrLive == ~(status = "live" /\ kind = "pr")
ProbeNoReloadFailUndo == ~(\E u \in Users : pc[u] = "Undo" /\ valid[disk])
ProbeNoStaleCatch == ~(\E u \in Users : reviewed[u] = "t" /\ pc[u] \in {"Write", "Publish"})
ProbeNoInvalidDraft == ~(\E u \in Users : pc[u] \in SaveSteps /\ ~valid[snap[u]])


vars == << valid, disk, branch, runtimeFile, up, epoch, crashes, fails, 
           thirdWrites, broken, status, content, base, saved, liveBytes, kind, 
           prState, fate, contains, ep, snap, reviewed, prev, stageHead, 
           badOverwrite, lostEdit, falseLive, pc >>

ProcSet == (Users) \cup {"gh"} \cup {"poller"} \cup {"third"} \cup {"crasher"} \cup {"restarter"}

Init == (* Global variables *)
        /\ valid \in {f \in [Vers -> BOOLEAN] : f["v0"] /\ f["t"] /\ ~f["torn"]}
        /\ disk = "v0"
        /\ branch = "v0"
        /\ runtimeFile = "v0"
        /\ up = TRUE
        /\ epoch = 0
        /\ crashes = 0
        /\ fails = 0
        /\ thirdWrites = 0
        /\ broken = FALSE
        /\ status = "editing"
        /\ content = "d"
        /\ base = "v0"
        /\ saved = "none"
        /\ liveBytes = "none"
        /\ kind = "none"
        /\ prState = "none"
        /\ fate = "none"
        /\ contains = FALSE
        /\ ep = [u \in Users |-> 0]
        /\ snap = [u \in Users |-> "none"]
        /\ reviewed = [u \in Users |-> "none"]
        /\ prev = [u \in Users |-> "none"]
        /\ stageHead = [u \in Users |-> "none"]
        /\ badOverwrite = FALSE
        /\ lostEdit = FALSE
        /\ falseLive = FALSE
        /\ pc = [self \in ProcSet |-> CASE self \in Users -> "Idle"
                                        [] self = "gh" -> "GhAct"
                                        [] self = "poller" -> "Poll"
                                        [] self = "third" -> "Third"
                                        [] self = "crasher" -> "Crash"
                                        [] self = "restarter" -> "Restart"]

Idle(self) == /\ pc[self] = "Idle"
              /\ \/ /\ up /\ status = "editing"
                    /\ ep' = [ep EXCEPT ![self] = epoch]
                    /\ IF Fix("cas")
                          THEN /\ status' = "saving"
                               /\ snap' = [snap EXCEPT ![self] = content]
                               /\ IF Mode = "ws"
                                     THEN /\ pc' = [pc EXCEPT ![self] = "Check"]
                                     ELSE /\ pc' = [pc EXCEPT ![self] = "Stage"]
                          ELSE /\ pc' = [pc EXCEPT ![self] = "Click2"]
                               /\ UNCHANGED << status, snap >>
                    /\ UNCHANGED <<content, prState>>
                 \/ /\ content = "d" /\ status \in {"editing", "saving", "waiting"}
                    /\ content' = "e"
                    /\ pc' = [pc EXCEPT ![self] = "Idle"]
                    /\ UNCHANGED <<status, prState, ep, snap>>
                 \/ /\ Fix("stopwait") /\ up /\ status = "waiting"
                    /\ status' = "editing"
                    /\ prState' = "none"
                    /\ pc' = [pc EXCEPT ![self] = "Idle"]
                    /\ UNCHANGED <<content, ep, snap>>
              /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                              crashes, fails, thirdWrites, broken, base, saved, 
                              liveBytes, kind, fate, contains, reviewed, prev, 
                              stageHead, badOverwrite, lostEdit, falseLive >>

Click2(self) == /\ pc[self] = "Click2"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ UNCHANGED status
                      ELSE /\ status' = "saving"
                           /\ snap' = [snap EXCEPT ![self] = content]
                           /\ IF Mode = "ws"
                                 THEN /\ pc' = [pc EXCEPT ![self] = "Check"]
                                 ELSE /\ pc' = [pc EXCEPT ![self] = "Stage"]
                           /\ UNCHANGED << ep, reviewed, prev, stageHead >>
                /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                                crashes, fails, thirdWrites, broken, content, 
                                base, saved, liveBytes, kind, prState, fate, 
                                contains, badOverwrite, lostEdit, falseLive >>

Check(self) == /\ pc[self] = "Check"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << fails, status >>
                     ELSE /\ IF Fix("prevalidate") /\ ~valid[snap[self]]
                                THEN /\ status' = "editing"
                                     /\ ep' = [ep EXCEPT ![self] = 0]
                                     /\ snap' = [snap EXCEPT ![self] = "none"]
                                     /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                     /\ prev' = [prev EXCEPT ![self] = "none"]
                                     /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                     /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                     /\ fails' = fails
                                ELSE /\ \/ /\ Fix("reloadfirst") /\ fails < MaxFail
                                           /\ fails' = fails + 1
                                           /\ status' = "editing"
                                           /\ ep' = [ep EXCEPT ![self] = 0]
                                           /\ snap' = [snap EXCEPT ![self] = "none"]
                                           /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                           /\ prev' = [prev EXCEPT ![self] = "none"]
                                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                        \/ /\ IF StaleTarget # base
                                                 THEN /\ reviewed' = [reviewed EXCEPT ![self] = StaleTarget]
                                                 ELSE /\ TRUE
                                                      /\ UNCHANGED reviewed
                                           /\ IF Fix("cmpwrite_nolock")
                                                 THEN /\ pc' = [pc EXCEPT ![self] = "Recheck"]
                                                 ELSE /\ pc' = [pc EXCEPT ![self] = "Write"]
                                           /\ UNCHANGED <<fails, status, ep, snap, prev, stageHead>>
               /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                               crashes, thirdWrites, broken, content, base, 
                               saved, liveBytes, kind, prState, fate, contains, 
                               badOverwrite, lostEdit, falseLive >>

Recheck(self) == /\ pc[self] = "Recheck"
                 /\ IF ep[self] # epoch
                       THEN /\ ep' = [ep EXCEPT ![self] = 0]
                            /\ snap' = [snap EXCEPT ![self] = "none"]
                            /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                            /\ prev' = [prev EXCEPT ![self] = "none"]
                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                            /\ UNCHANGED status
                       ELSE /\ IF ~OkToOverwrite(self)
                                  THEN /\ status' = "editing"
                                       /\ ep' = [ep EXCEPT ![self] = 0]
                                       /\ snap' = [snap EXCEPT ![self] = "none"]
                                       /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                       /\ prev' = [prev EXCEPT ![self] = "none"]
                                       /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                       /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                  ELSE /\ pc' = [pc EXCEPT ![self] = "Write"]
                                       /\ UNCHANGED << status, ep, snap, 
                                                       reviewed, prev, 
                                                       stageHead >>
                 /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                                 crashes, fails, thirdWrites, broken, content, 
                                 base, saved, liveBytes, kind, prState, fate, 
                                 contains, badOverwrite, lostEdit, falseLive >>

Write(self) == /\ pc[self] = "Write"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << disk, status, saved, badOverwrite >>
                     ELSE /\ IF Fix("cmpwrite") /\ ~OkToOverwrite(self)
                                THEN /\ status' = "editing"
                                     /\ ep' = [ep EXCEPT ![self] = 0]
                                     /\ snap' = [snap EXCEPT ![self] = "none"]
                                     /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                     /\ prev' = [prev EXCEPT ![self] = "none"]
                                     /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                     /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                     /\ UNCHANGED << disk, saved, badOverwrite >>
                                ELSE /\ IF ~OkToOverwrite(self)
                                           THEN /\ badOverwrite' = TRUE
                                           ELSE /\ TRUE
                                                /\ UNCHANGED badOverwrite
                                     /\ prev' = [prev EXCEPT ![self] = disk]
                                     /\ saved' = snap[self]
                                     /\ IF Fix("atomicwrite")
                                           THEN /\ disk' = snap[self]
                                                /\ pc' = [pc EXCEPT ![self] = "Reload"]
                                           ELSE /\ disk' = "torn"
                                                /\ pc' = [pc EXCEPT ![self] = "Write2"]
                                     /\ UNCHANGED << status, ep, snap, 
                                                     reviewed, stageHead >>
               /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                               fails, thirdWrites, broken, content, base, 
                               liveBytes, kind, prState, fate, contains, 
                               lostEdit, falseLive >>

Write2(self) == /\ pc[self] = "Write2"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ disk' = disk
                      ELSE /\ disk' = snap[self]
                           /\ pc' = [pc EXCEPT ![self] = "Reload"]
                           /\ UNCHANGED << ep, snap, reviewed, prev, stageHead >>
                /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                                fails, thirdWrites, broken, status, content, 
                                base, saved, liveBytes, kind, prState, fate, 
                                contains, badOverwrite, lostEdit, falseLive >>

Reload(self) == /\ pc[self] = "Reload"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ UNCHANGED << runtimeFile, fails, status, base, 
                                           liveBytes, falseLive >>
                      ELSE /\ IF valid[disk] /\ fails < MaxFail /\ ~Fix("reloadfirst")
                                 THEN /\ \/ /\ runtimeFile' = disk
                                            /\ IF Fix("snapcheck") /\ (content # saved \/ runtimeFile' # saved)
                                                  THEN /\ status' = "editing"
                                                       /\ base' = saved
                                                       /\ UNCHANGED << liveBytes, 
                                                                       falseLive >>
                                                  ELSE /\ status' = "live"
                                                       /\ liveBytes' = saved
                                                       /\ IF runtimeFile' # saved
                                                             THEN /\ falseLive' = TRUE
                                                             ELSE /\ TRUE
                                                                  /\ UNCHANGED falseLive
                                                       /\ base' = base
                                            /\ ep' = [ep EXCEPT ![self] = 0]
                                            /\ snap' = [snap EXCEPT ![self] = "none"]
                                            /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                            /\ prev' = [prev EXCEPT ![self] = "none"]
                                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                            /\ fails' = fails
                                         \/ /\ fails' = fails + 1
                                            /\ pc' = [pc EXCEPT ![self] = "Undo"]
                                            /\ UNCHANGED <<runtimeFile, status, base, liveBytes, ep, snap, reviewed, prev, stageHead, falseLive>>
                                 ELSE /\ IF valid[disk]
                                            THEN /\ runtimeFile' = disk
                                                 /\ IF Fix("snapcheck") /\ (content # saved \/ runtimeFile' # saved)
                                                       THEN /\ status' = "editing"
                                                            /\ base' = saved
                                                            /\ UNCHANGED << liveBytes, 
                                                                            falseLive >>
                                                       ELSE /\ status' = "live"
                                                            /\ liveBytes' = saved
                                                            /\ IF runtimeFile' # saved
                                                                  THEN /\ falseLive' = TRUE
                                                                  ELSE /\ TRUE
                                                                       /\ UNCHANGED falseLive
                                                            /\ base' = base
                                                 /\ ep' = [ep EXCEPT ![self] = 0]
                                                 /\ snap' = [snap EXCEPT ![self] = "none"]
                                                 /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                                 /\ prev' = [prev EXCEPT ![self] = "none"]
                                                 /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                 /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                            ELSE /\ pc' = [pc EXCEPT ![self] = "Undo"]
                                                 /\ UNCHANGED << runtimeFile, 
                                                                 status, base, 
                                                                 liveBytes, ep, 
                                                                 snap, 
                                                                 reviewed, 
                                                                 prev, 
                                                                 stageHead, 
                                                                 falseLive >>
                                      /\ fails' = fails
                /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, 
                                thirdWrites, broken, content, saved, kind, 
                                prState, fate, contains, badOverwrite, 
                                lostEdit >>

Undo(self) == /\ pc[self] = "Undo"
              /\ IF ep[self] # epoch
                    THEN /\ ep' = [ep EXCEPT ![self] = 0]
                         /\ snap' = [snap EXCEPT ![self] = "none"]
                         /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                         /\ prev' = [prev EXCEPT ![self] = "none"]
                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
                         /\ UNCHANGED << disk, status, lostEdit >>
                    ELSE /\ IF Fix("guardundo")
                               THEN /\ IF disk = snap[self]
                                          THEN /\ disk' = prev[self]
                                          ELSE /\ TRUE
                                               /\ disk' = disk
                                    /\ UNCHANGED lostEdit
                               ELSE /\ IF disk # snap[self]
                                          THEN /\ lostEdit' = TRUE
                                          ELSE /\ TRUE
                                               /\ UNCHANGED lostEdit
                                    /\ disk' = prev[self]
                         /\ status' = "editing"
                         /\ ep' = [ep EXCEPT ![self] = 0]
                         /\ snap' = [snap EXCEPT ![self] = "none"]
                         /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                         /\ prev' = [prev EXCEPT ![self] = "none"]
                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
              /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                              fails, thirdWrites, broken, content, base, saved, 
                              liveBytes, kind, prState, fate, contains, 
                              badOverwrite, falseLive >>

Stage(self) == /\ pc[self] = "Stage"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << status, kind >>
                     ELSE /\ IF ~valid[snap[self]]
                                THEN /\ status' = "editing"
                                     /\ ep' = [ep EXCEPT ![self] = 0]
                                     /\ snap' = [snap EXCEPT ![self] = "none"]
                                     /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                     /\ prev' = [prev EXCEPT ![self] = "none"]
                                     /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                     /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                     /\ kind' = kind
                                ELSE /\ stageHead' = [stageHead EXCEPT ![self] = branch]
                                     /\ LET cur == IF Fix("targetbase") THEN branch ELSE runtimeFile IN
                                          IF cur # base
                                             THEN /\ reviewed' = [reviewed EXCEPT ![self] = cur]
                                             ELSE /\ TRUE
                                                  /\ UNCHANGED reviewed
                                     /\ \E k \in Kinds:
                                          kind' = k
                                     /\ pc' = [pc EXCEPT ![self] = "Publish"]
                                     /\ UNCHANGED << status, ep, snap, prev >>
               /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                               crashes, fails, thirdWrites, broken, content, 
                               base, saved, liveBytes, prState, fate, contains, 
                               badOverwrite, lostEdit, falseLive >>

Publish(self) == /\ pc[self] = "Publish"
                 /\ IF ep[self] # epoch
                       THEN /\ ep' = [ep EXCEPT ![self] = 0]
                            /\ snap' = [snap EXCEPT ![self] = "none"]
                            /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                            /\ prev' = [prev EXCEPT ![self] = "none"]
                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                            /\ UNCHANGED << branch, status, saved, prState, 
                                            fate, contains, badOverwrite >>
                       ELSE /\ IF kind = "push"
                                  THEN /\ IF branch # stageHead[self]
                                             THEN /\ status' = "editing"
                                                  /\ ep' = [ep EXCEPT ![self] = 0]
                                                  /\ snap' = [snap EXCEPT ![self] = "none"]
                                                  /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                                  /\ prev' = [prev EXCEPT ![self] = "none"]
                                                  /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                  /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                                  /\ UNCHANGED << branch, 
                                                                  saved, 
                                                                  prState, 
                                                                  contains, 
                                                                  badOverwrite >>
                                             ELSE /\ IF stageHead[self] \notin {base, reviewed[self]}
                                                        THEN /\ badOverwrite' = TRUE
                                                        ELSE /\ TRUE
                                                             /\ UNCHANGED badOverwrite
                                                  /\ saved' = snap[self]
                                                  /\ branch' = snap[self]
                                                  /\ contains' = TRUE
                                                  /\ prState' = "none"
                                                  /\ status' = "waiting"
                                                  /\ ep' = [ep EXCEPT ![self] = 0]
                                                  /\ snap' = [snap EXCEPT ![self] = "none"]
                                                  /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                                  /\ prev' = [prev EXCEPT ![self] = "none"]
                                                  /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                  /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                       /\ fate' = fate
                                  ELSE /\ saved' = snap[self]
                                       /\ prState' = "open"
                                       /\ contains' = FALSE
                                       /\ status' = "waiting"
                                       /\ \E o \in Outcomes:
                                            fate' = o
                                       /\ ep' = [ep EXCEPT ![self] = 0]
                                       /\ snap' = [snap EXCEPT ![self] = "none"]
                                       /\ reviewed' = [reviewed EXCEPT ![self] = "none"]
                                       /\ prev' = [prev EXCEPT ![self] = "none"]
                                       /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                       /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                       /\ UNCHANGED << branch, badOverwrite >>
                 /\ UNCHANGED << valid, disk, runtimeFile, up, epoch, crashes, 
                                 fails, thirdWrites, broken, content, base, 
                                 liveBytes, kind, lostEdit, falseLive >>

user(self) == Idle(self) \/ Click2(self) \/ Check(self) \/ Recheck(self)
                 \/ Write(self) \/ Write2(self) \/ Reload(self)
                 \/ Undo(self) \/ Stage(self) \/ Publish(self)

GhAct == /\ pc["gh"] = "GhAct"
         /\ prState = "open" /\ fate # "none"
         /\ IF fate = "close"
               THEN /\ prState' = "closed"
                    /\ UNCHANGED << branch, contains >>
               ELSE /\ prState' = fate
                    /\ branch' = saved
                    /\ contains' = (fate = "merge")
         /\ pc' = [pc EXCEPT !["gh"] = "GhAct"]
         /\ UNCHANGED << valid, disk, runtimeFile, up, epoch, crashes, fails, 
                         thirdWrites, broken, status, content, base, saved, 
                         liveBytes, kind, fate, ep, snap, reviewed, prev, 
                         stageHead, badOverwrite, lostEdit, falseLive >>

gh == GhAct

Poll == /\ pc["poller"] = "Poll"
        /\ up /\ status = "waiting" /\ (Merged \/ ClosedSeen)
        /\ IF ClosedSeen
              THEN /\ status' = "editing"
                   /\ UNCHANGED << runtimeFile, fails, base, liveBytes, 
                                   falseLive >>
              ELSE /\ IF fails < MaxFail
                         THEN /\ \/ /\ runtimeFile' = branch
                                    /\ IF Fix("snapcheck") /\ (content # saved \/ runtimeFile' # saved)
                                          THEN /\ status' = "editing"
                                               /\ base' = saved
                                               /\ UNCHANGED << liveBytes, 
                                                               falseLive >>
                                          ELSE /\ status' = "live"
                                               /\ liveBytes' = saved
                                               /\ IF runtimeFile' # saved
                                                     THEN /\ falseLive' = TRUE
                                                     ELSE /\ TRUE
                                                          /\ UNCHANGED falseLive
                                               /\ base' = base
                                    /\ fails' = fails
                                 \/ /\ fails' = fails + 1
                                    /\ UNCHANGED <<runtimeFile, status, base, liveBytes, falseLive>>
                         ELSE /\ runtimeFile' = branch
                              /\ IF Fix("snapcheck") /\ (content # saved \/ runtimeFile' # saved)
                                    THEN /\ status' = "editing"
                                         /\ base' = saved
                                         /\ UNCHANGED << liveBytes, falseLive >>
                                    ELSE /\ status' = "live"
                                         /\ liveBytes' = saved
                                         /\ IF runtimeFile' # saved
                                               THEN /\ falseLive' = TRUE
                                               ELSE /\ TRUE
                                                    /\ UNCHANGED falseLive
                                         /\ base' = base
                              /\ fails' = fails
        /\ pc' = [pc EXCEPT !["poller"] = "Poll"]
        /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, thirdWrites, 
                        broken, content, saved, kind, prState, fate, contains, 
                        ep, snap, reviewed, prev, stageHead, badOverwrite, 
                        lostEdit >>

poller == Poll

Third == /\ pc["third"] = "Third"
         /\ \/ /\ thirdWrites < MaxThird
               /\ thirdWrites' = thirdWrites + 1
               /\ IF Mode = "ws"
                     THEN /\ disk' = "t"
                          /\ UNCHANGED branch
                     ELSE /\ branch' = "t"
                          /\ disk' = disk
               /\ UNCHANGED runtimeFile
            \/ /\ up
               /\ IF Mode = "ws"
                     THEN /\ IF valid[disk]
                                THEN /\ runtimeFile' = disk
                                ELSE /\ TRUE
                                     /\ UNCHANGED runtimeFile
                     ELSE /\ runtimeFile' = branch
               /\ UNCHANGED <<disk, branch, thirdWrites>>
         /\ pc' = [pc EXCEPT !["third"] = "Third"]
         /\ UNCHANGED << valid, up, epoch, crashes, fails, broken, status, 
                         content, base, saved, liveBytes, kind, prState, fate, 
                         contains, ep, snap, reviewed, prev, stageHead, 
                         badOverwrite, lostEdit, falseLive >>

third == Third

Crash == /\ pc["crasher"] = "Crash"
         /\ up /\ crashes < MaxCrash
         /\ up' = FALSE
         /\ epoch' = epoch + 1
         /\ crashes' = crashes + 1
         /\ pc' = [pc EXCEPT !["crasher"] = "Crash"]
         /\ UNCHANGED << valid, disk, branch, runtimeFile, fails, thirdWrites, 
                         broken, status, content, base, saved, liveBytes, kind, 
                         prState, fate, contains, ep, snap, reviewed, prev, 
                         stageHead, badOverwrite, lostEdit, falseLive >>

crasher == Crash

Restart == /\ pc["restarter"] = "Restart"
           /\ ~up
           /\ up' = TRUE
           /\ IF Mode = "ws"
                 THEN /\ IF valid[disk]
                            THEN /\ runtimeFile' = disk
                                 /\ UNCHANGED broken
                            ELSE /\ broken' = TRUE
                                 /\ UNCHANGED runtimeFile
                 ELSE /\ runtimeFile' = branch
                      /\ UNCHANGED broken
           /\ IF Fix("startreset") /\ status = "saving"
                 THEN /\ status' = "editing"
                 ELSE /\ TRUE
                      /\ UNCHANGED status
           /\ pc' = [pc EXCEPT !["restarter"] = "Restart"]
           /\ UNCHANGED << valid, disk, branch, epoch, crashes, fails, 
                           thirdWrites, content, base, saved, liveBytes, kind, 
                           prState, fate, contains, ep, snap, reviewed, prev, 
                           stageHead, badOverwrite, lostEdit, falseLive >>

restarter == Restart

Next == gh \/ poller \/ third \/ crasher \/ restarter
           \/ (\E self \in Users: user(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Users : WF_vars(user(self))
        /\ WF_vars(gh)
        /\ WF_vars(poller)
        /\ WF_vars(restarter)

\* END TRANSLATION 
=============================================================================
