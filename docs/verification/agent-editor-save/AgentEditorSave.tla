---------------------------- MODULE AgentEditorSave ----------------------------
(* Save path of one shared Agent Editor draft for one agent file. See README.md.
   Fixes selects design choices; Fixes = {} is the plan as first written. The code
   as built is Fixes = AsBuilt with EditLeaves = TRUE and Poll = FALSE.
   A browser disconnect ends the save request's context (MaxCancel of them):
     nocancel: every step after the claim ignores it (context.WithoutCancel);
     claimnocancel: so does the claim's own database commit (WithoutCancel before
     the claim). Without it, a disconnect while the claim commits lands the claim,
     loses its reply, and no save runs.
   Poll: a recurring merge check while waiting (else only right after the push and
     when a page opens the draft).
   StopFair: someone eventually clicks Stop waiting.
   EditLeaves: a content edit of a waiting or live draft returns it to editing.
   samepending: a merge check ends only the wait of the pending save it read. *)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Users, Mode, HasGh, Outcomes, Kinds, Fixes,
          MaxCrash, MaxFail, MaxThird, MaxCancel, MaxSaves,
          Poll, StopFair, EditLeaves

AllFixes == {"cas", "prevalidate", "atomicwrite", "targetbase", "cmpwrite_nolock",
             "reloadfirst", "noundo", "snapcheck", "startreset", "stopwait", "samepending",
             "nocancel", "claimnocancel"}
AsBuilt == AllFixes
\* the steps after the claim follow the request's cancellation (the first bug, isolated)
LaterStepsCancelled == AsBuilt \ {"nocancel"}
\* the claim's commit follows it (the code with context.WithoutCancel right after the claim)
ClaimCancelled == AsBuilt \ {"claimnocancel"}
\* the code before a merge check ended only the wait it read
WithoutSamePending == AsBuilt \ {"samepending"}

ASSUME Mode \in {"ws", "overlay"}
ASSUME HasGh \in BOOLEAN
ASSUME Outcomes \subseteq {"merge", "squash", "rebase", "close", "none"} /\ Outcomes # {}
ASSUME Kinds \subseteq {"push", "pr"} /\ Kinds # {}
ASSUME Fixes \subseteq AllFixes
ASSUME {MaxCrash, MaxFail, MaxThird, MaxCancel, MaxSaves} \subseteq Nat
ASSUME {Poll, StopFair, EditLeaves} \subseteq BOOLEAN

\* File contents: v0 = bytes when the draft began, t = a third party's edit,
\* d/e = draft contents (e = after a co-editor's later edit), torn = half-written file.
Vers == {"v0", "t", "d", "e", "torn"}

\* who runs CheckAgentDraftMerge: a save request right after its push, a page opening the draft, a poller
Checkers == Users \cup {"opener", "poller"}

(* --algorithm AgentEditorSave {
variables
  valid \in {f \in [Vers -> BOOLEAN] : f["v0"] /\ f["t"] /\ ~f["torn"]},
  disk = "v0",          \* workspace agents/<name>.md (Mode ws)
  branch = "v0",        \* agents/<name>.md at the overlay's pinned branch head (Mode overlay)
  runtimeFile = "v0",   \* copy the running server loaded (runtime dir), changes only on Reload/start
  up = TRUE, epoch = 0, crashes = 0, fails = 0, thirdWrites = 0, cancels = 0, saves = 0, broken = FALSE,
  status = "editing", content = "d",
  base = "v0",          \* draft.Bases[destination]: the file the next save expects there
  saved = "none", liveBytes = "none",
  revOK = FALSE,        \* no op since the save's claim other than the save's own moves (revision check)
  kind = "none", prState = "none", fate = "none", contains = FALSE,
  prBytes = "none",     \* bytes of the pull request GitHub may still merge (one PR tracked)
  ep = [u \in Users |-> 0], snap = [u \in Users |-> "none"],
  prev = [u \in Users |-> "none"], stageHead = [u \in Users |-> "none"],
  checked = [u \in Users |-> "none"], cancelled = [u \in Users |-> FALSE],
  \* a merge check in flight: what it read ("merged"/"closed"), the pending file it read,
  \* fresh = checkers that read the draft at its current revision, same = at its current pending save
  chk = [c \in Checkers |-> "idle"], chkFile = [c \in Checkers |-> "none"],
  fresh = {}, same = {},
  \* ghosts
  seen = {"v0"},        \* bytes the draft's people reviewed or saved
  dirty = FALSE,        \* content edited since the last claim
  liveEdited = FALSE,   \* a live draft was edited (probe)
  staleEnd = FALSE,     \* a merge check ended a wait for a save it did not read
  badOverwrite = FALSE, raced = FALSE, lostEdit = FALSE, falseLive = FALSE;

define {
  Fix(f) == f \in Fixes
  Cut(u) == ~Fix("nocancel") /\ cancelled[u]
  Source == IF Mode = "ws" THEN disk ELSE branch
  StaleTarget == IF Fix("targetbase") THEN disk ELSE runtimeFile
  OkToOverwrite == disk = base
  \* with gh a PR's state; else (push, or no gh) the pinned branch contains our commit
  Merged == IF HasGh /\ kind = "pr" THEN prState \in {"merge", "squash", "rebase"} ELSE contains
  ClosedSeen == HasGh /\ kind = "pr" /\ prState = "closed"
  \* a save request's steps; after the move to waiting it only runs the merge check (PostPush, PostPush2)
  SaveSteps == {"Check", "Recheck", "Write", "Write2", "Reload", "Undo", "Stage", "Publish", "PostPush", "PostPush2"}
  ClaimSteps == SaveSteps \ {"PostPush", "PostPush2"}
  AtRest == up /\ \A u \in Users : pc[u] = "Idle" \/ ep[u] # epoch

  TypeOK == disk \in Vers /\ branch \in Vers /\ runtimeFile \in Vers /\ base \in Vers
            /\ status \in {"editing", "saving", "waiting", "live"} /\ seen \subseteq Vers
            /\ fresh \subseteq Checkers /\ same \subseteq Checkers
  \* at rest the file the next start loads is valid; a start never breaks
  NoInvalidLive == AtRest => valid[Source]
  StartupOK == ~broken
  \* a save only replaces bytes someone on the draft reviewed or saved
  StaleNeedsReview == ~badOverwrite /\ ~raced
  \* the same, except a write that lands between the last check and the rename (accepted race)
  StaleNeedsReviewOutsideRace == ~badOverwrite
  \* one claimed save per draft; "live" means the draft's contents are what went live
  SaveExclusive == Cardinality({u \in Users : pc[u] \in ClaimSteps /\ ep[u] = epoch}) <= 1
  NoMixedLive == status = "live" => content = liveBytes
  HonestLive == ~falseLive
  NoUndoClobber == ~lostEdit
  UndoUnreachable == \A u \in Users : pc[u] # "Undo"
  \* a draft edited after its last claim is never shown live
  EditedNotLive == status = "live" => ~dirty
  \* a merge check never ends the wait of a later save
  NoStaleCheckEnd == ~staleEnd
  \* liveness
  WaitingResolves == (status = "waiting") ~> (status \in {"live", "editing"})
  SavingResolves == (status = "saving") ~> (status # "saving")
  \* once merged (or closed, seen with gh) the draft stops waiting without anyone acting
  MergeResolves == (status = "waiting" /\ (Merged \/ ClosedSeen)) ~> (status # "waiting")
  \* reachability probes: each must be VIOLATED (the state is reachable)
  ProbeNotLive == status # "live"
  ProbeNoCrashSave == ~(~up /\ \E u \in Users : pc[u] \in SaveSteps)
  ProbeNoPrLive == ~(status = "live" /\ kind = "pr")
  ProbeNoStaleCatch == ~("t" \in seen /\ \E u \in Users : pc[u] \in {"Write", "Publish"})
  ProbeNoInvalidDraft == ~(\E u \in Users : pc[u] \in SaveSteps /\ ~valid[snap[u]])
  ProbeNoLiveEdit == ~liveEdited
  \* a disconnected request still runs a save step, or the overlay push and its merge check
  ProbeNoCutSave == ~(\E u \in Users : cancelled[u] /\ pc[u] \in SaveSteps)
  ProbeNoCutPush == ~(\E u \in Users : cancelled[u] /\ pc[u] \in {"Publish", "PostPush", "PostPush2"})
  ProbeNoRetry == ~(\E u \in Users : pc[u] = "Stage" /\ stageHead[u] # "none")
  ProbeNoReloadFailEditing == ~(status = "editing" /\ base = saved /\ fails > 0 /\ runtimeFile # saved)
  \* a merge check that read an earlier pending save finishes while a later one waits
  ProbeNoStaleFinish == ~(status = "waiting" /\ \E c \in Checkers : chk[c] # "idle" /\ c \notin same)
}

\* workspace reloadAgentDraft's moves: live when unchanged since the claim and the loaded file is the
\* saved bytes, else editing; either way saved becomes the destination's expected file. ok = Reload succeeded.
macro MarkLive(ok) {
  if (Fix("snapcheck") /\ (~ok \/ ~revOK \/ runtimeFile # saved)) {
    status := "editing"; base := saved; fresh := {};
  } else {
    status := "live"; liveBytes := saved; base := saved; fresh := {};
    if (runtimeFile # saved) falseLive := TRUE;
  };
}

\* refuse(): back to editing; a cancelled request's move fails and the draft stays saving
macro Release() {
  if (~Cut(self)) { status := "editing"; fresh := {}; };
}

\* CheckAgentDraftMerge, first half: load the draft, ask gh / the branch
macro CheckStart(c) {
  if (status = "waiting" /\ (Merged \/ ClosedSeen)) {
    chk[c] := IF ClosedSeen THEN "closed" ELSE "merged";
    chkFile[c] := saved;
    if (revOK) fresh := fresh \cup {c};
    same := same \cup {c};
  };
}

\* second half: closed -> back to editing (needs status waiting); merged -> Reload, then
\* reloadAgentDraft's moves: live at the pending revision with the loaded file = pending file, else
\* editing while the draft is (still, or again) waiting
macro CheckFinish(c) {
  if (chk[c] = "closed") {
    if (status = "waiting" /\ (~Fix("samepending") \/ c \in same)) {
      if (c \notin same) staleEnd := TRUE;
      status := "editing"; fresh := {};
    } else fresh := fresh \ {c};
  } else if (chk[c] = "merged") {
    with (ok \in IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}) {
      if (ok) runtimeFile := branch; else fails := fails + 1;
      if (~Fix("snapcheck") \/ (ok /\ c \in fresh /\ runtimeFile = chkFile[c])) {
        status := "live"; liveBytes := chkFile[c]; base := chkFile[c]; fresh := {};
        if (runtimeFile # chkFile[c]) falseLive := TRUE;
      } else if (status = "waiting" /\ (~Fix("samepending") \/ c \in same)) {
        if (c \notin same) staleEnd := TRUE;
        status := "editing"; base := chkFile[c]; fresh := {};
      } else fresh := fresh \ {c};
    };
  };
  chk[c] := "idle"; chkFile[c] := "none"; same := same \ {c};
}

\* end of a save request: drop its scratch values (keeps idle states canonical)
macro Done() {
  ep[self] := 0; snap[self] := "none"; prev[self] := "none";
  stageHead[self] := "none"; checked[self] := "none"; cancelled[self] := FALSE;
}

fair process (user \in Users) {
Idle:
  \* Save button: the claim; allowed unless saving or waiting (MaxSaves clicks in all)
  await up /\ status \in {"editing", "live"} /\ saves < MaxSaves;
  saves := saves + 1;
  if (Fix("cas")) {
    status := "saving"; revOK := TRUE; dirty := FALSE; fresh := {};
    either {
      ep[self] := epoch; snap[self] := content;
      if (Mode = "ws") goto Check; else goto Stage;
    } or {
      \* the request ends while the claim commits: as built the save runs on; else the claim lands,
      \* its reply is lost and no save runs
      await cancels < MaxCancel;
      cancels := cancels + 1;
      if (Fix("claimnocancel")) {
        ep[self] := epoch; snap[self] := content; cancelled[self] := TRUE;
        if (Mode = "ws") goto Check; else goto Stage;
      } else goto Idle;
    };
  } else { ep[self] := epoch; goto Click2; };
Click2:
  \* plan: status was read in Idle; it is set here in a separate write (no CAS)
  if (ep[self] # epoch) { Done(); goto Idle; }
  else {
    status := "saving"; snap[self] := content; revOK := TRUE; dirty := FALSE; fresh := {};
    if (Mode = "ws") goto Check; else goto Stage;
  };
Check:
  \* staleness check (+ review), loader validation of the draft alone, Reload's check on a stage
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Fix("prevalidate") /\ ~valid[snap[self]]) { Release(); Done(); goto Idle; }
  else {
    either {
      \* reloadfirst: Reload's own validation runs on a stage holding the draft, before any write
      await Fix("reloadfirst") /\ fails < MaxFail;
      fails := fails + 1; Release(); Done(); goto Idle;
    } or {
      \* review accepted: the reviewed file becomes the expected one (the claim op records it)
      if (StaleTarget # base) { base := StaleTarget; seen := seen \cup {StaleTarget}; };
      if (Fix("cmpwrite_nolock")) goto Recheck; else goto Write;
    };
  };
Recheck:
  \* as built: compare again after writing the temp file, right before the rename, without a lock
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (~OkToOverwrite) { Release(); Done(); goto Idle; }
  else { checked[self] := disk; goto Write; };
Write:
  \* plan: os.Root WriteFile (truncate, then fill); atomicwrite: temp file + rename
  if (ep[self] # epoch) { Done(); goto Idle; }
  else {
    if (disk \notin seen) {
      if (Fix("cmpwrite_nolock") /\ disk # checked[self]) raced := TRUE;
      else badOverwrite := TRUE;
    };
    prev[self] := disk; saved := snap[self]; seen := seen \cup {snap[self]};
    if (Fix("atomicwrite")) { disk := snap[self]; goto Reload; }
    else { disk := "torn"; goto Write2; };
  };
Write2:
  if (ep[self] # epoch) { Done(); goto Idle; }
  else { disk := snap[self]; goto Reload; };
Reload:
  \* validated Reload: fails on an invalid file, or (bounded) for reasons outside this file
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Fix("noundo")) {
    \* as built: a failed Reload leaves the written file; MarkLive(FALSE) goes back to editing
    with (ok \in IF ~valid[disk] THEN {FALSE} ELSE IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}) {
      if (ok) runtimeFile := disk; else if (valid[disk]) fails := fails + 1;
      if (~Cut(self)) MarkLive(ok);
    };
    Done(); goto Idle;
  }
  else if (valid[disk] /\ fails < MaxFail /\ ~Fix("reloadfirst")) {
    either { runtimeFile := disk; if (~Cut(self)) MarkLive(TRUE); Done(); goto Idle; }
    or { fails := fails + 1; goto Undo; };
  } else if (valid[disk]) { runtimeFile := disk; if (~Cut(self)) MarkLive(TRUE); Done(); goto Idle; }
  else goto Undo;
Undo:
  \* plan: put the old bytes back
  if (ep[self] # epoch) { Done(); goto Idle; }
  else {
    if (disk # snap[self]) lostEdit := TRUE;
    disk := prev[self];
    Release(); Done(); goto Idle;
  };
Stage:
  \* clone pinned branch, staleness check (+ review), validate with the loader; again after a rejected push
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Cut(self)) { Done(); goto Idle; }  \* git clone killed; the refusal's move fails too
  else if (~valid[snap[self]]) { Release(); Done(); goto Idle; }
  else {
    if (stageHead[self] = "none") { with (k \in Kinds) { kind := k; }; };
    stageHead[self] := branch;
    with (cur = IF Fix("targetbase") THEN branch ELSE runtimeFile) {
      if (cur # base) { base := cur; seen := seen \cup {cur}; };
    };
    goto Publish;
  };
Publish:
  if (ep[self] # epoch) { Done(); goto Idle; }
  else if (Cut(self)) {
    \* the cancelled request kills git push / gh pr create, which may already have done their work;
    \* the move to waiting then fails and the draft stays saving
    if (kind = "push" /\ branch = stageHead[self] /\ stageHead[self] # snap[self]) {
      either { if (stageHead[self] \notin seen) badOverwrite := TRUE; branch := snap[self]; } or { skip; };
    } else if (kind = "pr" /\ stageHead[self] # snap[self]) {
      either { prBytes := snap[self]; prState := "open"; with (o \in Outcomes) { fate := o; }; } or { skip; };
    };
    Done(); goto Idle;
  }
  else if (stageHead[self] = snap[self]) {
    \* the branch already holds this file: no commit
    if (kind = "pr") { Release(); Done(); goto Idle; }
    else {
      saved := snap[self]; seen := seen \cup {snap[self]}; contains := TRUE;
      status := "waiting"; fresh := {}; same := {}; goto PostPush;
    };
  }
  else if (kind = "push") {
    \* never forced: a moved branch rejects the push and the save clones and checks again
    if (branch # stageHead[self]) goto Stage;
    else {
      if (stageHead[self] \notin seen) badOverwrite := TRUE;
      saved := snap[self]; seen := seen \cup {snap[self]}; branch := snap[self];
      contains := TRUE; status := "waiting"; fresh := {}; same := {}; goto PostPush;
    };
  } else {
    \* PR mode: a branch of the claim's own, so the push is never rejected
    saved := snap[self]; seen := seen \cup {snap[self]}; prBytes := snap[self];
    prState := "open"; contains := FALSE; status := "waiting"; fresh := {}; same := {};
    with (o \in Outcomes) { fate := o; };
    goto PostPush;
  };
PostPush:
  \* the save request runs CheckAgentDraftMerge once right after the push
  if (ep[self] # epoch \/ Cut(self)) { Done(); goto Idle; }
  else { CheckStart(self); goto PostPush2; };
PostPush2:
  if (ep[self] # epoch) {
    chk[self] := "idle"; chkFile[self] := "none"; fresh := fresh \ {self}; same := same \ {self};
    Done(); goto Idle;
  } else if (Cut(self)) {
    \* Reload runs (it takes no context), the moves fail
    if (chk[self] = "merged") { either { runtimeFile := branch; } or { skip; }; };
    chk[self] := "idle"; chkFile[self] := "none"; fresh := fresh \ {self}; same := same \ {self};
    Done(); goto Idle;
  } else { CheckFinish(self); Done(); goto Idle; };
}

\* GitHub: eventually applies the PR's fate, picked from Outcomes when the PR opened ("none" = never acts)
fair process (gh = "gh") {
GhAct:
  while (TRUE) {
    await prState = "open" /\ fate # "none";
    if (fate = "close") prState := "closed";
    else {
      prState := fate; branch := prBytes;
      if (fate = "merge") contains := TRUE;
    };
  };
}

\* GetAgentDraft with since = 0: a page opening the draft runs the merge check (nobody has to)
process (opener = "opener") {
OpenStart:
  while (TRUE) {
    await up /\ status = "waiting" /\ (Merged \/ ClosedSeen);
    CheckStart("opener");
OpenFinish:
    await up;
    CheckFinish("opener");
  };
}

\* not built (user decision): a recurring check while waiting; the plan-as-written checks keep it
fair process (poller = "poller") {
PollStart:
  while (TRUE) {
    await Poll /\ up /\ status = "waiting" /\ (Merged \/ ClosedSeen);
    CheckStart("poller");
PollFinish:
    await up;
    CheckFinish("poller");
  };
}

\* co-editors: one content edit at any time; External MCP / test chat ops bump the revision only
process (coeditor = "coeditor") {
CoEdit:
  while (TRUE) {
    either {
      await up /\ content = "d";
      content := "e"; revOK := FALSE; dirty := TRUE; fresh := {};
      if (status = "live") liveEdited := TRUE;
      if (EditLeaves /\ status \in {"waiting", "live"}) status := "editing";
    } or {
      await up /\ revOK /\ status \in {"saving", "waiting"};
      revOK := FALSE; fresh := {};
    };
  };
}

\* Stop waiting (the PR stays open on GitHub)
process (stopper = "stopper") {
Stop:
  while (TRUE) {
    await Fix("stopwait") /\ up /\ status = "waiting";
    status := "editing"; fresh := {};
  };
}

\* the same click, by someone who eventually makes it (StopFair)
fair process (patient = "patient") {
Patient:
  while (TRUE) {
    await StopFair /\ Fix("stopwait") /\ up /\ status = "waiting";
    status := "editing"; fresh := {};
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

\* the browser drops a save request after its claim (tab closed, navigation)
process (canceller = "canceller") {
Cancel:
  while (TRUE) {
    await cancels < MaxCancel;
    with (u \in {v \in Users : pc[v] \in SaveSteps /\ ep[v] = epoch /\ ~cancelled[v]}) {
      cancelled[u] := TRUE; cancels := cancels + 1;
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
    if (Fix("startreset") /\ status = "saving") { status := "editing"; fresh := {}; };
  };
}
} *)
\* BEGIN TRANSLATION
VARIABLES pc, valid, disk, branch, runtimeFile, up, epoch, crashes, fails, 
          thirdWrites, cancels, saves, broken, status, content, base, saved, 
          liveBytes, revOK, kind, prState, fate, contains, prBytes, ep, snap, 
          prev, stageHead, checked, cancelled, chk, chkFile, fresh, same, 
          seen, dirty, liveEdited, staleEnd, badOverwrite, raced, lostEdit, 
          falseLive

(* define statement *)
Fix(f) == f \in Fixes
Cut(u) == ~Fix("nocancel") /\ cancelled[u]
Source == IF Mode = "ws" THEN disk ELSE branch
StaleTarget == IF Fix("targetbase") THEN disk ELSE runtimeFile
OkToOverwrite == disk = base

Merged == IF HasGh /\ kind = "pr" THEN prState \in {"merge", "squash", "rebase"} ELSE contains
ClosedSeen == HasGh /\ kind = "pr" /\ prState = "closed"

SaveSteps == {"Check", "Recheck", "Write", "Write2", "Reload", "Undo", "Stage", "Publish", "PostPush", "PostPush2"}
ClaimSteps == SaveSteps \ {"PostPush", "PostPush2"}
AtRest == up /\ \A u \in Users : pc[u] = "Idle" \/ ep[u] # epoch

TypeOK == disk \in Vers /\ branch \in Vers /\ runtimeFile \in Vers /\ base \in Vers
          /\ status \in {"editing", "saving", "waiting", "live"} /\ seen \subseteq Vers
          /\ fresh \subseteq Checkers /\ same \subseteq Checkers

NoInvalidLive == AtRest => valid[Source]
StartupOK == ~broken

StaleNeedsReview == ~badOverwrite /\ ~raced

StaleNeedsReviewOutsideRace == ~badOverwrite

SaveExclusive == Cardinality({u \in Users : pc[u] \in ClaimSteps /\ ep[u] = epoch}) <= 1
NoMixedLive == status = "live" => content = liveBytes
HonestLive == ~falseLive
NoUndoClobber == ~lostEdit
UndoUnreachable == \A u \in Users : pc[u] # "Undo"

EditedNotLive == status = "live" => ~dirty

NoStaleCheckEnd == ~staleEnd

WaitingResolves == (status = "waiting") ~> (status \in {"live", "editing"})
SavingResolves == (status = "saving") ~> (status # "saving")

MergeResolves == (status = "waiting" /\ (Merged \/ ClosedSeen)) ~> (status # "waiting")

ProbeNotLive == status # "live"
ProbeNoCrashSave == ~(~up /\ \E u \in Users : pc[u] \in SaveSteps)
ProbeNoPrLive == ~(status = "live" /\ kind = "pr")
ProbeNoStaleCatch == ~("t" \in seen /\ \E u \in Users : pc[u] \in {"Write", "Publish"})
ProbeNoInvalidDraft == ~(\E u \in Users : pc[u] \in SaveSteps /\ ~valid[snap[u]])
ProbeNoLiveEdit == ~liveEdited

ProbeNoCutSave == ~(\E u \in Users : cancelled[u] /\ pc[u] \in SaveSteps)
ProbeNoCutPush == ~(\E u \in Users : cancelled[u] /\ pc[u] \in {"Publish", "PostPush", "PostPush2"})
ProbeNoRetry == ~(\E u \in Users : pc[u] = "Stage" /\ stageHead[u] # "none")
ProbeNoReloadFailEditing == ~(status = "editing" /\ base = saved /\ fails > 0 /\ runtimeFile # saved)

ProbeNoStaleFinish == ~(status = "waiting" /\ \E c \in Checkers : chk[c] # "idle" /\ c \notin same)


vars == << pc, valid, disk, branch, runtimeFile, up, epoch, crashes, fails, 
           thirdWrites, cancels, saves, broken, status, content, base, saved, 
           liveBytes, revOK, kind, prState, fate, contains, prBytes, ep, snap, 
           prev, stageHead, checked, cancelled, chk, chkFile, fresh, same, 
           seen, dirty, liveEdited, staleEnd, badOverwrite, raced, lostEdit, 
           falseLive >>

ProcSet == (Users) \cup {"gh"} \cup {"opener"} \cup {"poller"} \cup {"coeditor"} \cup {"stopper"} \cup {"patient"} \cup {"third"} \cup {"canceller"} \cup {"crasher"} \cup {"restarter"}

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
        /\ cancels = 0
        /\ saves = 0
        /\ broken = FALSE
        /\ status = "editing"
        /\ content = "d"
        /\ base = "v0"
        /\ saved = "none"
        /\ liveBytes = "none"
        /\ revOK = FALSE
        /\ kind = "none"
        /\ prState = "none"
        /\ fate = "none"
        /\ contains = FALSE
        /\ prBytes = "none"
        /\ ep = [u \in Users |-> 0]
        /\ snap = [u \in Users |-> "none"]
        /\ prev = [u \in Users |-> "none"]
        /\ stageHead = [u \in Users |-> "none"]
        /\ checked = [u \in Users |-> "none"]
        /\ cancelled = [u \in Users |-> FALSE]
        /\ chk = [c \in Checkers |-> "idle"]
        /\ chkFile = [c \in Checkers |-> "none"]
        /\ fresh = {}
        /\ same = {}
        /\ seen = {"v0"}
        /\ dirty = FALSE
        /\ liveEdited = FALSE
        /\ staleEnd = FALSE
        /\ badOverwrite = FALSE
        /\ raced = FALSE
        /\ lostEdit = FALSE
        /\ falseLive = FALSE
        /\ pc = [self \in ProcSet |-> CASE self \in Users -> "Idle"
                                        [] self = "gh" -> "GhAct"
                                        [] self = "opener" -> "OpenStart"
                                        [] self = "poller" -> "PollStart"
                                        [] self = "coeditor" -> "CoEdit"
                                        [] self = "stopper" -> "Stop"
                                        [] self = "patient" -> "Patient"
                                        [] self = "third" -> "Third"
                                        [] self = "canceller" -> "Cancel"
                                        [] self = "crasher" -> "Crash"
                                        [] self = "restarter" -> "Restart"]

Idle(self) == /\ pc[self] = "Idle"
              /\ up /\ status \in {"editing", "live"} /\ saves < MaxSaves
              /\ saves' = saves + 1
              /\ IF Fix("cas")
                    THEN /\ status' = "saving"
                         /\ revOK' = TRUE
                         /\ dirty' = FALSE
                         /\ fresh' = {}
                         /\ \/ /\ ep' = [ep EXCEPT ![self] = epoch]
                               /\ snap' = [snap EXCEPT ![self] = content]
                               /\ IF Mode = "ws"
                                     THEN /\ pc' = [pc EXCEPT ![self] = "Check"]
                                     ELSE /\ pc' = [pc EXCEPT ![self] = "Stage"]
                               /\ UNCHANGED <<cancels, cancelled>>
                            \/ /\ cancels < MaxCancel
                               /\ cancels' = cancels + 1
                               /\ IF Fix("claimnocancel")
                                     THEN /\ ep' = [ep EXCEPT ![self] = epoch]
                                          /\ snap' = [snap EXCEPT ![self] = content]
                                          /\ cancelled' = [cancelled EXCEPT ![self] = TRUE]
                                          /\ IF Mode = "ws"
                                                THEN /\ pc' = [pc EXCEPT ![self] = "Check"]
                                                ELSE /\ pc' = [pc EXCEPT ![self] = "Stage"]
                                     ELSE /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                          /\ UNCHANGED << ep, snap, cancelled >>
                    ELSE /\ ep' = [ep EXCEPT ![self] = epoch]
                         /\ pc' = [pc EXCEPT ![self] = "Click2"]
                         /\ UNCHANGED << cancels, status, revOK, snap, 
                                         cancelled, fresh, dirty >>
              /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                              crashes, fails, thirdWrites, broken, content, 
                              base, saved, liveBytes, kind, prState, fate, 
                              contains, prBytes, prev, stageHead, checked, chk, 
                              chkFile, same, seen, liveEdited, staleEnd, 
                              badOverwrite, raced, lostEdit, falseLive >>

Click2(self) == /\ pc[self] = "Click2"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ checked' = [checked EXCEPT ![self] = "none"]
                           /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ UNCHANGED << status, revOK, fresh, dirty >>
                      ELSE /\ status' = "saving"
                           /\ snap' = [snap EXCEPT ![self] = content]
                           /\ revOK' = TRUE
                           /\ dirty' = FALSE
                           /\ fresh' = {}
                           /\ IF Mode = "ws"
                                 THEN /\ pc' = [pc EXCEPT ![self] = "Check"]
                                 ELSE /\ pc' = [pc EXCEPT ![self] = "Stage"]
                           /\ UNCHANGED << ep, prev, stageHead, checked, 
                                           cancelled >>
                /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                                crashes, fails, thirdWrites, cancels, saves, 
                                broken, content, base, saved, liveBytes, kind, 
                                prState, fate, contains, prBytes, chk, chkFile, 
                                same, seen, liveEdited, staleEnd, badOverwrite, 
                                raced, lostEdit, falseLive >>

Check(self) == /\ pc[self] = "Check"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ checked' = [checked EXCEPT ![self] = "none"]
                          /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << fails, status, base, fresh, seen >>
                     ELSE /\ IF Fix("prevalidate") /\ ~valid[snap[self]]
                                THEN /\ IF ~Cut(self)
                                           THEN /\ status' = "editing"
                                                /\ fresh' = {}
                                           ELSE /\ TRUE
                                                /\ UNCHANGED << status, fresh >>
                                     /\ ep' = [ep EXCEPT ![self] = 0]
                                     /\ snap' = [snap EXCEPT ![self] = "none"]
                                     /\ prev' = [prev EXCEPT ![self] = "none"]
                                     /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                     /\ checked' = [checked EXCEPT ![self] = "none"]
                                     /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                     /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                     /\ UNCHANGED << fails, base, seen >>
                                ELSE /\ \/ /\ Fix("reloadfirst") /\ fails < MaxFail
                                           /\ fails' = fails + 1
                                           /\ IF ~Cut(self)
                                                 THEN /\ status' = "editing"
                                                      /\ fresh' = {}
                                                 ELSE /\ TRUE
                                                      /\ UNCHANGED << status, 
                                                                      fresh >>
                                           /\ ep' = [ep EXCEPT ![self] = 0]
                                           /\ snap' = [snap EXCEPT ![self] = "none"]
                                           /\ prev' = [prev EXCEPT ![self] = "none"]
                                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                           /\ checked' = [checked EXCEPT ![self] = "none"]
                                           /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                           /\ UNCHANGED <<base, seen>>
                                        \/ /\ IF StaleTarget # base
                                                 THEN /\ base' = StaleTarget
                                                      /\ seen' = (seen \cup {StaleTarget})
                                                 ELSE /\ TRUE
                                                      /\ UNCHANGED << base, 
                                                                      seen >>
                                           /\ IF Fix("cmpwrite_nolock")
                                                 THEN /\ pc' = [pc EXCEPT ![self] = "Recheck"]
                                                 ELSE /\ pc' = [pc EXCEPT ![self] = "Write"]
                                           /\ UNCHANGED <<fails, status, ep, snap, prev, stageHead, checked, cancelled, fresh>>
               /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                               crashes, thirdWrites, cancels, saves, broken, 
                               content, saved, liveBytes, revOK, kind, prState, 
                               fate, contains, prBytes, chk, chkFile, same, 
                               dirty, liveEdited, staleEnd, badOverwrite, 
                               raced, lostEdit, falseLive >>

Recheck(self) == /\ pc[self] = "Recheck"
                 /\ IF ep[self] # epoch
                       THEN /\ ep' = [ep EXCEPT ![self] = 0]
                            /\ snap' = [snap EXCEPT ![self] = "none"]
                            /\ prev' = [prev EXCEPT ![self] = "none"]
                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                            /\ checked' = [checked EXCEPT ![self] = "none"]
                            /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                            /\ UNCHANGED << status, fresh >>
                       ELSE /\ IF ~OkToOverwrite
                                  THEN /\ IF ~Cut(self)
                                             THEN /\ status' = "editing"
                                                  /\ fresh' = {}
                                             ELSE /\ TRUE
                                                  /\ UNCHANGED << status, 
                                                                  fresh >>
                                       /\ ep' = [ep EXCEPT ![self] = 0]
                                       /\ snap' = [snap EXCEPT ![self] = "none"]
                                       /\ prev' = [prev EXCEPT ![self] = "none"]
                                       /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                       /\ checked' = [checked EXCEPT ![self] = "none"]
                                       /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                       /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                  ELSE /\ checked' = [checked EXCEPT ![self] = disk]
                                       /\ pc' = [pc EXCEPT ![self] = "Write"]
                                       /\ UNCHANGED << status, ep, snap, prev, 
                                                       stageHead, cancelled, 
                                                       fresh >>
                 /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                                 crashes, fails, thirdWrites, cancels, saves, 
                                 broken, content, base, saved, liveBytes, 
                                 revOK, kind, prState, fate, contains, prBytes, 
                                 chk, chkFile, same, seen, dirty, liveEdited, 
                                 staleEnd, badOverwrite, raced, lostEdit, 
                                 falseLive >>

Write(self) == /\ pc[self] = "Write"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ checked' = [checked EXCEPT ![self] = "none"]
                          /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << disk, saved, seen, badOverwrite, 
                                          raced >>
                     ELSE /\ IF disk \notin seen
                                THEN /\ IF Fix("cmpwrite_nolock") /\ disk # checked[self]
                                           THEN /\ raced' = TRUE
                                                /\ UNCHANGED badOverwrite
                                           ELSE /\ badOverwrite' = TRUE
                                                /\ raced' = raced
                                ELSE /\ TRUE
                                     /\ UNCHANGED << badOverwrite, raced >>
                          /\ prev' = [prev EXCEPT ![self] = disk]
                          /\ saved' = snap[self]
                          /\ seen' = (seen \cup {snap[self]})
                          /\ IF Fix("atomicwrite")
                                THEN /\ disk' = snap[self]
                                     /\ pc' = [pc EXCEPT ![self] = "Reload"]
                                ELSE /\ disk' = "torn"
                                     /\ pc' = [pc EXCEPT ![self] = "Write2"]
                          /\ UNCHANGED << ep, snap, stageHead, checked, 
                                          cancelled >>
               /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                               fails, thirdWrites, cancels, saves, broken, 
                               status, content, base, liveBytes, revOK, kind, 
                               prState, fate, contains, prBytes, chk, chkFile, 
                               fresh, same, dirty, liveEdited, staleEnd, 
                               lostEdit, falseLive >>

Write2(self) == /\ pc[self] = "Write2"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ checked' = [checked EXCEPT ![self] = "none"]
                           /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ disk' = disk
                      ELSE /\ disk' = snap[self]
                           /\ pc' = [pc EXCEPT ![self] = "Reload"]
                           /\ UNCHANGED << ep, snap, prev, stageHead, checked, 
                                           cancelled >>
                /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                                fails, thirdWrites, cancels, saves, broken, 
                                status, content, base, saved, liveBytes, revOK, 
                                kind, prState, fate, contains, prBytes, chk, 
                                chkFile, fresh, same, seen, dirty, liveEdited, 
                                staleEnd, badOverwrite, raced, lostEdit, 
                                falseLive >>

Reload(self) == /\ pc[self] = "Reload"
                /\ IF ep[self] # epoch
                      THEN /\ ep' = [ep EXCEPT ![self] = 0]
                           /\ snap' = [snap EXCEPT ![self] = "none"]
                           /\ prev' = [prev EXCEPT ![self] = "none"]
                           /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                           /\ checked' = [checked EXCEPT ![self] = "none"]
                           /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                           /\ pc' = [pc EXCEPT ![self] = "Idle"]
                           /\ UNCHANGED << runtimeFile, fails, status, base, 
                                           liveBytes, fresh, falseLive >>
                      ELSE /\ IF Fix("noundo")
                                 THEN /\ \E ok \in IF ~valid[disk] THEN {FALSE} ELSE IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}:
                                           /\ IF ok
                                                 THEN /\ runtimeFile' = disk
                                                      /\ fails' = fails
                                                 ELSE /\ IF valid[disk]
                                                            THEN /\ fails' = fails + 1
                                                            ELSE /\ TRUE
                                                                 /\ fails' = fails
                                                      /\ UNCHANGED runtimeFile
                                           /\ IF ~Cut(self)
                                                 THEN /\ IF Fix("snapcheck") /\ (~ok \/ ~revOK \/ runtimeFile' # saved)
                                                            THEN /\ status' = "editing"
                                                                 /\ base' = saved
                                                                 /\ fresh' = {}
                                                                 /\ UNCHANGED << liveBytes, 
                                                                                 falseLive >>
                                                            ELSE /\ status' = "live"
                                                                 /\ liveBytes' = saved
                                                                 /\ base' = saved
                                                                 /\ fresh' = {}
                                                                 /\ IF runtimeFile' # saved
                                                                       THEN /\ falseLive' = TRUE
                                                                       ELSE /\ TRUE
                                                                            /\ UNCHANGED falseLive
                                                 ELSE /\ TRUE
                                                      /\ UNCHANGED << status, 
                                                                      base, 
                                                                      liveBytes, 
                                                                      fresh, 
                                                                      falseLive >>
                                      /\ ep' = [ep EXCEPT ![self] = 0]
                                      /\ snap' = [snap EXCEPT ![self] = "none"]
                                      /\ prev' = [prev EXCEPT ![self] = "none"]
                                      /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                      /\ checked' = [checked EXCEPT ![self] = "none"]
                                      /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                      /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                 ELSE /\ IF valid[disk] /\ fails < MaxFail /\ ~Fix("reloadfirst")
                                            THEN /\ \/ /\ runtimeFile' = disk
                                                       /\ IF ~Cut(self)
                                                             THEN /\ IF Fix("snapcheck") /\ (~TRUE \/ ~revOK \/ runtimeFile' # saved)
                                                                        THEN /\ status' = "editing"
                                                                             /\ base' = saved
                                                                             /\ fresh' = {}
                                                                             /\ UNCHANGED << liveBytes, 
                                                                                             falseLive >>
                                                                        ELSE /\ status' = "live"
                                                                             /\ liveBytes' = saved
                                                                             /\ base' = saved
                                                                             /\ fresh' = {}
                                                                             /\ IF runtimeFile' # saved
                                                                                   THEN /\ falseLive' = TRUE
                                                                                   ELSE /\ TRUE
                                                                                        /\ UNCHANGED falseLive
                                                             ELSE /\ TRUE
                                                                  /\ UNCHANGED << status, 
                                                                                  base, 
                                                                                  liveBytes, 
                                                                                  fresh, 
                                                                                  falseLive >>
                                                       /\ ep' = [ep EXCEPT ![self] = 0]
                                                       /\ snap' = [snap EXCEPT ![self] = "none"]
                                                       /\ prev' = [prev EXCEPT ![self] = "none"]
                                                       /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                       /\ checked' = [checked EXCEPT ![self] = "none"]
                                                       /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                                       /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                                       /\ fails' = fails
                                                    \/ /\ fails' = fails + 1
                                                       /\ pc' = [pc EXCEPT ![self] = "Undo"]
                                                       /\ UNCHANGED <<runtimeFile, status, base, liveBytes, ep, snap, prev, stageHead, checked, cancelled, fresh, falseLive>>
                                            ELSE /\ IF valid[disk]
                                                       THEN /\ runtimeFile' = disk
                                                            /\ IF ~Cut(self)
                                                                  THEN /\ IF Fix("snapcheck") /\ (~TRUE \/ ~revOK \/ runtimeFile' # saved)
                                                                             THEN /\ status' = "editing"
                                                                                  /\ base' = saved
                                                                                  /\ fresh' = {}
                                                                                  /\ UNCHANGED << liveBytes, 
                                                                                                  falseLive >>
                                                                             ELSE /\ status' = "live"
                                                                                  /\ liveBytes' = saved
                                                                                  /\ base' = saved
                                                                                  /\ fresh' = {}
                                                                                  /\ IF runtimeFile' # saved
                                                                                        THEN /\ falseLive' = TRUE
                                                                                        ELSE /\ TRUE
                                                                                             /\ UNCHANGED falseLive
                                                                  ELSE /\ TRUE
                                                                       /\ UNCHANGED << status, 
                                                                                       base, 
                                                                                       liveBytes, 
                                                                                       fresh, 
                                                                                       falseLive >>
                                                            /\ ep' = [ep EXCEPT ![self] = 0]
                                                            /\ snap' = [snap EXCEPT ![self] = "none"]
                                                            /\ prev' = [prev EXCEPT ![self] = "none"]
                                                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                            /\ checked' = [checked EXCEPT ![self] = "none"]
                                                            /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                                       ELSE /\ pc' = [pc EXCEPT ![self] = "Undo"]
                                                            /\ UNCHANGED << runtimeFile, 
                                                                            status, 
                                                                            base, 
                                                                            liveBytes, 
                                                                            ep, 
                                                                            snap, 
                                                                            prev, 
                                                                            stageHead, 
                                                                            checked, 
                                                                            cancelled, 
                                                                            fresh, 
                                                                            falseLive >>
                                                 /\ fails' = fails
                /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, 
                                thirdWrites, cancels, saves, broken, content, 
                                saved, revOK, kind, prState, fate, contains, 
                                prBytes, chk, chkFile, same, seen, dirty, 
                                liveEdited, staleEnd, badOverwrite, raced, 
                                lostEdit >>

Undo(self) == /\ pc[self] = "Undo"
              /\ IF ep[self] # epoch
                    THEN /\ ep' = [ep EXCEPT ![self] = 0]
                         /\ snap' = [snap EXCEPT ![self] = "none"]
                         /\ prev' = [prev EXCEPT ![self] = "none"]
                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                         /\ checked' = [checked EXCEPT ![self] = "none"]
                         /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
                         /\ UNCHANGED << disk, status, fresh, lostEdit >>
                    ELSE /\ IF disk # snap[self]
                               THEN /\ lostEdit' = TRUE
                               ELSE /\ TRUE
                                    /\ UNCHANGED lostEdit
                         /\ disk' = prev[self]
                         /\ IF ~Cut(self)
                               THEN /\ status' = "editing"
                                    /\ fresh' = {}
                               ELSE /\ TRUE
                                    /\ UNCHANGED << status, fresh >>
                         /\ ep' = [ep EXCEPT ![self] = 0]
                         /\ snap' = [snap EXCEPT ![self] = "none"]
                         /\ prev' = [prev EXCEPT ![self] = "none"]
                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                         /\ checked' = [checked EXCEPT ![self] = "none"]
                         /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
              /\ UNCHANGED << valid, branch, runtimeFile, up, epoch, crashes, 
                              fails, thirdWrites, cancels, saves, broken, 
                              content, base, saved, liveBytes, revOK, kind, 
                              prState, fate, contains, prBytes, chk, chkFile, 
                              same, seen, dirty, liveEdited, staleEnd, 
                              badOverwrite, raced, falseLive >>

Stage(self) == /\ pc[self] = "Stage"
               /\ IF ep[self] # epoch
                     THEN /\ ep' = [ep EXCEPT ![self] = 0]
                          /\ snap' = [snap EXCEPT ![self] = "none"]
                          /\ prev' = [prev EXCEPT ![self] = "none"]
                          /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                          /\ checked' = [checked EXCEPT ![self] = "none"]
                          /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                          /\ pc' = [pc EXCEPT ![self] = "Idle"]
                          /\ UNCHANGED << status, base, kind, fresh, seen >>
                     ELSE /\ IF Cut(self)
                                THEN /\ ep' = [ep EXCEPT ![self] = 0]
                                     /\ snap' = [snap EXCEPT ![self] = "none"]
                                     /\ prev' = [prev EXCEPT ![self] = "none"]
                                     /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                     /\ checked' = [checked EXCEPT ![self] = "none"]
                                     /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                     /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                     /\ UNCHANGED << status, base, kind, fresh, 
                                                     seen >>
                                ELSE /\ IF ~valid[snap[self]]
                                           THEN /\ IF ~Cut(self)
                                                      THEN /\ status' = "editing"
                                                           /\ fresh' = {}
                                                      ELSE /\ TRUE
                                                           /\ UNCHANGED << status, 
                                                                           fresh >>
                                                /\ ep' = [ep EXCEPT ![self] = 0]
                                                /\ snap' = [snap EXCEPT ![self] = "none"]
                                                /\ prev' = [prev EXCEPT ![self] = "none"]
                                                /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                /\ checked' = [checked EXCEPT ![self] = "none"]
                                                /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                                /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                                /\ UNCHANGED << base, kind, 
                                                                seen >>
                                           ELSE /\ IF stageHead[self] = "none"
                                                      THEN /\ \E k \in Kinds:
                                                                kind' = k
                                                      ELSE /\ TRUE
                                                           /\ kind' = kind
                                                /\ stageHead' = [stageHead EXCEPT ![self] = branch]
                                                /\ LET cur == IF Fix("targetbase") THEN branch ELSE runtimeFile IN
                                                     IF cur # base
                                                        THEN /\ base' = cur
                                                             /\ seen' = (seen \cup {cur})
                                                        ELSE /\ TRUE
                                                             /\ UNCHANGED << base, 
                                                                             seen >>
                                                /\ pc' = [pc EXCEPT ![self] = "Publish"]
                                                /\ UNCHANGED << status, ep, 
                                                                snap, prev, 
                                                                checked, 
                                                                cancelled, 
                                                                fresh >>
               /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                               crashes, fails, thirdWrites, cancels, saves, 
                               broken, content, saved, liveBytes, revOK, 
                               prState, fate, contains, prBytes, chk, chkFile, 
                               same, dirty, liveEdited, staleEnd, badOverwrite, 
                               raced, lostEdit, falseLive >>

Publish(self) == /\ pc[self] = "Publish"
                 /\ IF ep[self] # epoch
                       THEN /\ ep' = [ep EXCEPT ![self] = 0]
                            /\ snap' = [snap EXCEPT ![self] = "none"]
                            /\ prev' = [prev EXCEPT ![self] = "none"]
                            /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                            /\ checked' = [checked EXCEPT ![self] = "none"]
                            /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                            /\ pc' = [pc EXCEPT ![self] = "Idle"]
                            /\ UNCHANGED << branch, status, saved, prState, 
                                            fate, contains, prBytes, fresh, 
                                            same, seen, badOverwrite >>
                       ELSE /\ IF Cut(self)
                                  THEN /\ IF kind = "push" /\ branch = stageHead[self] /\ stageHead[self] # snap[self]
                                             THEN /\ \/ /\ IF stageHead[self] \notin seen
                                                              THEN /\ badOverwrite' = TRUE
                                                              ELSE /\ TRUE
                                                                   /\ UNCHANGED badOverwrite
                                                        /\ branch' = snap[self]
                                                     \/ /\ TRUE
                                                        /\ UNCHANGED <<branch, badOverwrite>>
                                                  /\ UNCHANGED << prState, 
                                                                  fate, 
                                                                  prBytes >>
                                             ELSE /\ IF kind = "pr" /\ stageHead[self] # snap[self]
                                                        THEN /\ \/ /\ prBytes' = snap[self]
                                                                   /\ prState' = "open"
                                                                   /\ \E o \in Outcomes:
                                                                        fate' = o
                                                                \/ /\ TRUE
                                                                   /\ UNCHANGED <<prState, fate, prBytes>>
                                                        ELSE /\ TRUE
                                                             /\ UNCHANGED << prState, 
                                                                             fate, 
                                                                             prBytes >>
                                                  /\ UNCHANGED << branch, 
                                                                  badOverwrite >>
                                       /\ ep' = [ep EXCEPT ![self] = 0]
                                       /\ snap' = [snap EXCEPT ![self] = "none"]
                                       /\ prev' = [prev EXCEPT ![self] = "none"]
                                       /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                       /\ checked' = [checked EXCEPT ![self] = "none"]
                                       /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                       /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                       /\ UNCHANGED << status, saved, contains, 
                                                       fresh, same, seen >>
                                  ELSE /\ IF stageHead[self] = snap[self]
                                             THEN /\ IF kind = "pr"
                                                        THEN /\ IF ~Cut(self)
                                                                   THEN /\ status' = "editing"
                                                                        /\ fresh' = {}
                                                                   ELSE /\ TRUE
                                                                        /\ UNCHANGED << status, 
                                                                                        fresh >>
                                                             /\ ep' = [ep EXCEPT ![self] = 0]
                                                             /\ snap' = [snap EXCEPT ![self] = "none"]
                                                             /\ prev' = [prev EXCEPT ![self] = "none"]
                                                             /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                                             /\ checked' = [checked EXCEPT ![self] = "none"]
                                                             /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                                             /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                                             /\ UNCHANGED << saved, 
                                                                             contains, 
                                                                             same, 
                                                                             seen >>
                                                        ELSE /\ saved' = snap[self]
                                                             /\ seen' = (seen \cup {snap[self]})
                                                             /\ contains' = TRUE
                                                             /\ status' = "waiting"
                                                             /\ fresh' = {}
                                                             /\ same' = {}
                                                             /\ pc' = [pc EXCEPT ![self] = "PostPush"]
                                                             /\ UNCHANGED << ep, 
                                                                             snap, 
                                                                             prev, 
                                                                             stageHead, 
                                                                             checked, 
                                                                             cancelled >>
                                                  /\ UNCHANGED << branch, 
                                                                  prState, 
                                                                  fate, 
                                                                  prBytes, 
                                                                  badOverwrite >>
                                             ELSE /\ IF kind = "push"
                                                        THEN /\ IF branch # stageHead[self]
                                                                   THEN /\ pc' = [pc EXCEPT ![self] = "Stage"]
                                                                        /\ UNCHANGED << branch, 
                                                                                        status, 
                                                                                        saved, 
                                                                                        contains, 
                                                                                        fresh, 
                                                                                        same, 
                                                                                        seen, 
                                                                                        badOverwrite >>
                                                                   ELSE /\ IF stageHead[self] \notin seen
                                                                              THEN /\ badOverwrite' = TRUE
                                                                              ELSE /\ TRUE
                                                                                   /\ UNCHANGED badOverwrite
                                                                        /\ saved' = snap[self]
                                                                        /\ seen' = (seen \cup {snap[self]})
                                                                        /\ branch' = snap[self]
                                                                        /\ contains' = TRUE
                                                                        /\ status' = "waiting"
                                                                        /\ fresh' = {}
                                                                        /\ same' = {}
                                                                        /\ pc' = [pc EXCEPT ![self] = "PostPush"]
                                                             /\ UNCHANGED << prState, 
                                                                             fate, 
                                                                             prBytes >>
                                                        ELSE /\ saved' = snap[self]
                                                             /\ seen' = (seen \cup {snap[self]})
                                                             /\ prBytes' = snap[self]
                                                             /\ prState' = "open"
                                                             /\ contains' = FALSE
                                                             /\ status' = "waiting"
                                                             /\ fresh' = {}
                                                             /\ same' = {}
                                                             /\ \E o \in Outcomes:
                                                                  fate' = o
                                                             /\ pc' = [pc EXCEPT ![self] = "PostPush"]
                                                             /\ UNCHANGED << branch, 
                                                                             badOverwrite >>
                                                  /\ UNCHANGED << ep, snap, 
                                                                  prev, 
                                                                  stageHead, 
                                                                  checked, 
                                                                  cancelled >>
                 /\ UNCHANGED << valid, disk, runtimeFile, up, epoch, crashes, 
                                 fails, thirdWrites, cancels, saves, broken, 
                                 content, base, liveBytes, revOK, kind, chk, 
                                 chkFile, dirty, liveEdited, staleEnd, raced, 
                                 lostEdit, falseLive >>

PostPush(self) == /\ pc[self] = "PostPush"
                  /\ IF ep[self] # epoch \/ Cut(self)
                        THEN /\ ep' = [ep EXCEPT ![self] = 0]
                             /\ snap' = [snap EXCEPT ![self] = "none"]
                             /\ prev' = [prev EXCEPT ![self] = "none"]
                             /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                             /\ checked' = [checked EXCEPT ![self] = "none"]
                             /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                             /\ pc' = [pc EXCEPT ![self] = "Idle"]
                             /\ UNCHANGED << chk, chkFile, fresh, same >>
                        ELSE /\ IF status = "waiting" /\ (Merged \/ ClosedSeen)
                                   THEN /\ chk' = [chk EXCEPT ![self] = IF ClosedSeen THEN "closed" ELSE "merged"]
                                        /\ chkFile' = [chkFile EXCEPT ![self] = saved]
                                        /\ IF revOK
                                              THEN /\ fresh' = (fresh \cup {self})
                                              ELSE /\ TRUE
                                                   /\ fresh' = fresh
                                        /\ same' = (same \cup {self})
                                   ELSE /\ TRUE
                                        /\ UNCHANGED << chk, chkFile, fresh, 
                                                        same >>
                             /\ pc' = [pc EXCEPT ![self] = "PostPush2"]
                             /\ UNCHANGED << ep, snap, prev, stageHead, 
                                             checked, cancelled >>
                  /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                                  crashes, fails, thirdWrites, cancels, saves, 
                                  broken, status, content, base, saved, 
                                  liveBytes, revOK, kind, prState, fate, 
                                  contains, prBytes, seen, dirty, liveEdited, 
                                  staleEnd, badOverwrite, raced, lostEdit, 
                                  falseLive >>

PostPush2(self) == /\ pc[self] = "PostPush2"
                   /\ IF ep[self] # epoch
                         THEN /\ chk' = [chk EXCEPT ![self] = "idle"]
                              /\ chkFile' = [chkFile EXCEPT ![self] = "none"]
                              /\ fresh' = fresh \ {self}
                              /\ same' = same \ {self}
                              /\ ep' = [ep EXCEPT ![self] = 0]
                              /\ snap' = [snap EXCEPT ![self] = "none"]
                              /\ prev' = [prev EXCEPT ![self] = "none"]
                              /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                              /\ checked' = [checked EXCEPT ![self] = "none"]
                              /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                              /\ pc' = [pc EXCEPT ![self] = "Idle"]
                              /\ UNCHANGED << runtimeFile, fails, status, base, 
                                              liveBytes, staleEnd, falseLive >>
                         ELSE /\ IF Cut(self)
                                    THEN /\ IF chk[self] = "merged"
                                               THEN /\ \/ /\ runtimeFile' = branch
                                                       \/ /\ TRUE
                                                          /\ UNCHANGED runtimeFile
                                               ELSE /\ TRUE
                                                    /\ UNCHANGED runtimeFile
                                         /\ chk' = [chk EXCEPT ![self] = "idle"]
                                         /\ chkFile' = [chkFile EXCEPT ![self] = "none"]
                                         /\ fresh' = fresh \ {self}
                                         /\ same' = same \ {self}
                                         /\ ep' = [ep EXCEPT ![self] = 0]
                                         /\ snap' = [snap EXCEPT ![self] = "none"]
                                         /\ prev' = [prev EXCEPT ![self] = "none"]
                                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                         /\ checked' = [checked EXCEPT ![self] = "none"]
                                         /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
                                         /\ UNCHANGED << fails, status, base, 
                                                         liveBytes, staleEnd, 
                                                         falseLive >>
                                    ELSE /\ IF chk[self] = "closed"
                                               THEN /\ IF status = "waiting" /\ (~Fix("samepending") \/ self \in same)
                                                          THEN /\ IF self \notin same
                                                                     THEN /\ staleEnd' = TRUE
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED staleEnd
                                                               /\ status' = "editing"
                                                               /\ fresh' = {}
                                                          ELSE /\ fresh' = fresh \ {self}
                                                               /\ UNCHANGED << status, 
                                                                               staleEnd >>
                                                    /\ UNCHANGED << runtimeFile, 
                                                                    fails, 
                                                                    base, 
                                                                    liveBytes, 
                                                                    falseLive >>
                                               ELSE /\ IF chk[self] = "merged"
                                                          THEN /\ \E ok \in IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}:
                                                                    /\ IF ok
                                                                          THEN /\ runtimeFile' = branch
                                                                               /\ fails' = fails
                                                                          ELSE /\ fails' = fails + 1
                                                                               /\ UNCHANGED runtimeFile
                                                                    /\ IF ~Fix("snapcheck") \/ (ok /\ self \in fresh /\ runtimeFile' = chkFile[self])
                                                                          THEN /\ status' = "live"
                                                                               /\ liveBytes' = chkFile[self]
                                                                               /\ base' = chkFile[self]
                                                                               /\ fresh' = {}
                                                                               /\ IF runtimeFile' # chkFile[self]
                                                                                     THEN /\ falseLive' = TRUE
                                                                                     ELSE /\ TRUE
                                                                                          /\ UNCHANGED falseLive
                                                                               /\ UNCHANGED staleEnd
                                                                          ELSE /\ IF status = "waiting" /\ (~Fix("samepending") \/ self \in same)
                                                                                     THEN /\ IF self \notin same
                                                                                                THEN /\ staleEnd' = TRUE
                                                                                                ELSE /\ TRUE
                                                                                                     /\ UNCHANGED staleEnd
                                                                                          /\ status' = "editing"
                                                                                          /\ base' = chkFile[self]
                                                                                          /\ fresh' = {}
                                                                                     ELSE /\ fresh' = fresh \ {self}
                                                                                          /\ UNCHANGED << status, 
                                                                                                          base, 
                                                                                                          staleEnd >>
                                                                               /\ UNCHANGED << liveBytes, 
                                                                                               falseLive >>
                                                          ELSE /\ TRUE
                                                               /\ UNCHANGED << runtimeFile, 
                                                                               fails, 
                                                                               status, 
                                                                               base, 
                                                                               liveBytes, 
                                                                               fresh, 
                                                                               staleEnd, 
                                                                               falseLive >>
                                         /\ chk' = [chk EXCEPT ![self] = "idle"]
                                         /\ chkFile' = [chkFile EXCEPT ![self] = "none"]
                                         /\ same' = same \ {self}
                                         /\ ep' = [ep EXCEPT ![self] = 0]
                                         /\ snap' = [snap EXCEPT ![self] = "none"]
                                         /\ prev' = [prev EXCEPT ![self] = "none"]
                                         /\ stageHead' = [stageHead EXCEPT ![self] = "none"]
                                         /\ checked' = [checked EXCEPT ![self] = "none"]
                                         /\ cancelled' = [cancelled EXCEPT ![self] = FALSE]
                                         /\ pc' = [pc EXCEPT ![self] = "Idle"]
                   /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, 
                                   thirdWrites, cancels, saves, broken, 
                                   content, saved, revOK, kind, prState, fate, 
                                   contains, prBytes, seen, dirty, liveEdited, 
                                   badOverwrite, raced, lostEdit >>

user(self) == Idle(self) \/ Click2(self) \/ Check(self) \/ Recheck(self)
                 \/ Write(self) \/ Write2(self) \/ Reload(self)
                 \/ Undo(self) \/ Stage(self) \/ Publish(self)
                 \/ PostPush(self) \/ PostPush2(self)

GhAct == /\ pc["gh"] = "GhAct"
         /\ prState = "open" /\ fate # "none"
         /\ IF fate = "close"
               THEN /\ prState' = "closed"
                    /\ UNCHANGED << branch, contains >>
               ELSE /\ prState' = fate
                    /\ branch' = prBytes
                    /\ IF fate = "merge"
                          THEN /\ contains' = TRUE
                          ELSE /\ TRUE
                               /\ UNCHANGED contains
         /\ pc' = [pc EXCEPT !["gh"] = "GhAct"]
         /\ UNCHANGED << valid, disk, runtimeFile, up, epoch, crashes, fails, 
                         thirdWrites, cancels, saves, broken, status, content, 
                         base, saved, liveBytes, revOK, kind, fate, prBytes, 
                         ep, snap, prev, stageHead, checked, cancelled, chk, 
                         chkFile, fresh, same, seen, dirty, liveEdited, 
                         staleEnd, badOverwrite, raced, lostEdit, falseLive >>

gh == GhAct

OpenStart == /\ pc["opener"] = "OpenStart"
             /\ up /\ status = "waiting" /\ (Merged \/ ClosedSeen)
             /\ IF status = "waiting" /\ (Merged \/ ClosedSeen)
                   THEN /\ chk' = [chk EXCEPT !["opener"] = IF ClosedSeen THEN "closed" ELSE "merged"]
                        /\ chkFile' = [chkFile EXCEPT !["opener"] = saved]
                        /\ IF revOK
                              THEN /\ fresh' = (fresh \cup {"opener"})
                              ELSE /\ TRUE
                                   /\ fresh' = fresh
                        /\ same' = (same \cup {"opener"})
                   ELSE /\ TRUE
                        /\ UNCHANGED << chk, chkFile, fresh, same >>
             /\ pc' = [pc EXCEPT !["opener"] = "OpenFinish"]
             /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                             crashes, fails, thirdWrites, cancels, saves, 
                             broken, status, content, base, saved, liveBytes, 
                             revOK, kind, prState, fate, contains, prBytes, ep, 
                             snap, prev, stageHead, checked, cancelled, seen, 
                             dirty, liveEdited, staleEnd, badOverwrite, raced, 
                             lostEdit, falseLive >>

OpenFinish == /\ pc["opener"] = "OpenFinish"
              /\ up
              /\ IF chk["opener"] = "closed"
                    THEN /\ IF status = "waiting" /\ (~Fix("samepending") \/ "opener" \in same)
                               THEN /\ IF "opener" \notin same
                                          THEN /\ staleEnd' = TRUE
                                          ELSE /\ TRUE
                                               /\ UNCHANGED staleEnd
                                    /\ status' = "editing"
                                    /\ fresh' = {}
                               ELSE /\ fresh' = fresh \ {"opener"}
                                    /\ UNCHANGED << status, staleEnd >>
                         /\ UNCHANGED << runtimeFile, fails, base, liveBytes, 
                                         falseLive >>
                    ELSE /\ IF chk["opener"] = "merged"
                               THEN /\ \E ok \in IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}:
                                         /\ IF ok
                                               THEN /\ runtimeFile' = branch
                                                    /\ fails' = fails
                                               ELSE /\ fails' = fails + 1
                                                    /\ UNCHANGED runtimeFile
                                         /\ IF ~Fix("snapcheck") \/ (ok /\ "opener" \in fresh /\ runtimeFile' = chkFile["opener"])
                                               THEN /\ status' = "live"
                                                    /\ liveBytes' = chkFile["opener"]
                                                    /\ base' = chkFile["opener"]
                                                    /\ fresh' = {}
                                                    /\ IF runtimeFile' # chkFile["opener"]
                                                          THEN /\ falseLive' = TRUE
                                                          ELSE /\ TRUE
                                                               /\ UNCHANGED falseLive
                                                    /\ UNCHANGED staleEnd
                                               ELSE /\ IF status = "waiting" /\ (~Fix("samepending") \/ "opener" \in same)
                                                          THEN /\ IF "opener" \notin same
                                                                     THEN /\ staleEnd' = TRUE
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED staleEnd
                                                               /\ status' = "editing"
                                                               /\ base' = chkFile["opener"]
                                                               /\ fresh' = {}
                                                          ELSE /\ fresh' = fresh \ {"opener"}
                                                               /\ UNCHANGED << status, 
                                                                               base, 
                                                                               staleEnd >>
                                                    /\ UNCHANGED << liveBytes, 
                                                                    falseLive >>
                               ELSE /\ TRUE
                                    /\ UNCHANGED << runtimeFile, fails, status, 
                                                    base, liveBytes, fresh, 
                                                    staleEnd, falseLive >>
              /\ chk' = [chk EXCEPT !["opener"] = "idle"]
              /\ chkFile' = [chkFile EXCEPT !["opener"] = "none"]
              /\ same' = same \ {"opener"}
              /\ pc' = [pc EXCEPT !["opener"] = "OpenStart"]
              /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, 
                              thirdWrites, cancels, saves, broken, content, 
                              saved, revOK, kind, prState, fate, contains, 
                              prBytes, ep, snap, prev, stageHead, checked, 
                              cancelled, seen, dirty, liveEdited, badOverwrite, 
                              raced, lostEdit >>

opener == OpenStart \/ OpenFinish

PollStart == /\ pc["poller"] = "PollStart"
             /\ Poll /\ up /\ status = "waiting" /\ (Merged \/ ClosedSeen)
             /\ IF status = "waiting" /\ (Merged \/ ClosedSeen)
                   THEN /\ chk' = [chk EXCEPT !["poller"] = IF ClosedSeen THEN "closed" ELSE "merged"]
                        /\ chkFile' = [chkFile EXCEPT !["poller"] = saved]
                        /\ IF revOK
                              THEN /\ fresh' = (fresh \cup {"poller"})
                              ELSE /\ TRUE
                                   /\ fresh' = fresh
                        /\ same' = (same \cup {"poller"})
                   ELSE /\ TRUE
                        /\ UNCHANGED << chk, chkFile, fresh, same >>
             /\ pc' = [pc EXCEPT !["poller"] = "PollFinish"]
             /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                             crashes, fails, thirdWrites, cancels, saves, 
                             broken, status, content, base, saved, liveBytes, 
                             revOK, kind, prState, fate, contains, prBytes, ep, 
                             snap, prev, stageHead, checked, cancelled, seen, 
                             dirty, liveEdited, staleEnd, badOverwrite, raced, 
                             lostEdit, falseLive >>

PollFinish == /\ pc["poller"] = "PollFinish"
              /\ up
              /\ IF chk["poller"] = "closed"
                    THEN /\ IF status = "waiting" /\ (~Fix("samepending") \/ "poller" \in same)
                               THEN /\ IF "poller" \notin same
                                          THEN /\ staleEnd' = TRUE
                                          ELSE /\ TRUE
                                               /\ UNCHANGED staleEnd
                                    /\ status' = "editing"
                                    /\ fresh' = {}
                               ELSE /\ fresh' = fresh \ {"poller"}
                                    /\ UNCHANGED << status, staleEnd >>
                         /\ UNCHANGED << runtimeFile, fails, base, liveBytes, 
                                         falseLive >>
                    ELSE /\ IF chk["poller"] = "merged"
                               THEN /\ \E ok \in IF fails < MaxFail THEN BOOLEAN ELSE {TRUE}:
                                         /\ IF ok
                                               THEN /\ runtimeFile' = branch
                                                    /\ fails' = fails
                                               ELSE /\ fails' = fails + 1
                                                    /\ UNCHANGED runtimeFile
                                         /\ IF ~Fix("snapcheck") \/ (ok /\ "poller" \in fresh /\ runtimeFile' = chkFile["poller"])
                                               THEN /\ status' = "live"
                                                    /\ liveBytes' = chkFile["poller"]
                                                    /\ base' = chkFile["poller"]
                                                    /\ fresh' = {}
                                                    /\ IF runtimeFile' # chkFile["poller"]
                                                          THEN /\ falseLive' = TRUE
                                                          ELSE /\ TRUE
                                                               /\ UNCHANGED falseLive
                                                    /\ UNCHANGED staleEnd
                                               ELSE /\ IF status = "waiting" /\ (~Fix("samepending") \/ "poller" \in same)
                                                          THEN /\ IF "poller" \notin same
                                                                     THEN /\ staleEnd' = TRUE
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED staleEnd
                                                               /\ status' = "editing"
                                                               /\ base' = chkFile["poller"]
                                                               /\ fresh' = {}
                                                          ELSE /\ fresh' = fresh \ {"poller"}
                                                               /\ UNCHANGED << status, 
                                                                               base, 
                                                                               staleEnd >>
                                                    /\ UNCHANGED << liveBytes, 
                                                                    falseLive >>
                               ELSE /\ TRUE
                                    /\ UNCHANGED << runtimeFile, fails, status, 
                                                    base, liveBytes, fresh, 
                                                    staleEnd, falseLive >>
              /\ chk' = [chk EXCEPT !["poller"] = "idle"]
              /\ chkFile' = [chkFile EXCEPT !["poller"] = "none"]
              /\ same' = same \ {"poller"}
              /\ pc' = [pc EXCEPT !["poller"] = "PollStart"]
              /\ UNCHANGED << valid, disk, branch, up, epoch, crashes, 
                              thirdWrites, cancels, saves, broken, content, 
                              saved, revOK, kind, prState, fate, contains, 
                              prBytes, ep, snap, prev, stageHead, checked, 
                              cancelled, seen, dirty, liveEdited, badOverwrite, 
                              raced, lostEdit >>

poller == PollStart \/ PollFinish

CoEdit == /\ pc["coeditor"] = "CoEdit"
          /\ \/ /\ up /\ content = "d"
                /\ content' = "e"
                /\ revOK' = FALSE
                /\ dirty' = TRUE
                /\ fresh' = {}
                /\ IF status = "live"
                      THEN /\ liveEdited' = TRUE
                      ELSE /\ TRUE
                           /\ UNCHANGED liveEdited
                /\ IF EditLeaves /\ status \in {"waiting", "live"}
                      THEN /\ status' = "editing"
                      ELSE /\ TRUE
                           /\ UNCHANGED status
             \/ /\ up /\ revOK /\ status \in {"saving", "waiting"}
                /\ revOK' = FALSE
                /\ fresh' = {}
                /\ UNCHANGED <<status, content, dirty, liveEdited>>
          /\ pc' = [pc EXCEPT !["coeditor"] = "CoEdit"]
          /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, crashes, 
                          fails, thirdWrites, cancels, saves, broken, base, 
                          saved, liveBytes, kind, prState, fate, contains, 
                          prBytes, ep, snap, prev, stageHead, checked, 
                          cancelled, chk, chkFile, same, seen, staleEnd, 
                          badOverwrite, raced, lostEdit, falseLive >>

coeditor == CoEdit

Stop == /\ pc["stopper"] = "Stop"
        /\ Fix("stopwait") /\ up /\ status = "waiting"
        /\ status' = "editing"
        /\ fresh' = {}
        /\ pc' = [pc EXCEPT !["stopper"] = "Stop"]
        /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, crashes, 
                        fails, thirdWrites, cancels, saves, broken, content, 
                        base, saved, liveBytes, revOK, kind, prState, fate, 
                        contains, prBytes, ep, snap, prev, stageHead, checked, 
                        cancelled, chk, chkFile, same, seen, dirty, liveEdited, 
                        staleEnd, badOverwrite, raced, lostEdit, falseLive >>

stopper == Stop

Patient == /\ pc["patient"] = "Patient"
           /\ StopFair /\ Fix("stopwait") /\ up /\ status = "waiting"
           /\ status' = "editing"
           /\ fresh' = {}
           /\ pc' = [pc EXCEPT !["patient"] = "Patient"]
           /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, 
                           crashes, fails, thirdWrites, cancels, saves, broken, 
                           content, base, saved, liveBytes, revOK, kind, 
                           prState, fate, contains, prBytes, ep, snap, prev, 
                           stageHead, checked, cancelled, chk, chkFile, same, 
                           seen, dirty, liveEdited, staleEnd, badOverwrite, 
                           raced, lostEdit, falseLive >>

patient == Patient

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
         /\ UNCHANGED << valid, up, epoch, crashes, fails, cancels, saves, 
                         broken, status, content, base, saved, liveBytes, 
                         revOK, kind, prState, fate, contains, prBytes, ep, 
                         snap, prev, stageHead, checked, cancelled, chk, 
                         chkFile, fresh, same, seen, dirty, liveEdited, 
                         staleEnd, badOverwrite, raced, lostEdit, falseLive >>

third == Third

Cancel == /\ pc["canceller"] = "Cancel"
          /\ cancels < MaxCancel
          /\ \E u \in {v \in Users : pc[v] \in SaveSteps /\ ep[v] = epoch /\ ~cancelled[v]}:
               /\ cancelled' = [cancelled EXCEPT ![u] = TRUE]
               /\ cancels' = cancels + 1
          /\ pc' = [pc EXCEPT !["canceller"] = "Cancel"]
          /\ UNCHANGED << valid, disk, branch, runtimeFile, up, epoch, crashes, 
                          fails, thirdWrites, saves, broken, status, content, 
                          base, saved, liveBytes, revOK, kind, prState, fate, 
                          contains, prBytes, ep, snap, prev, stageHead, 
                          checked, chk, chkFile, fresh, same, seen, dirty, 
                          liveEdited, staleEnd, badOverwrite, raced, lostEdit, 
                          falseLive >>

canceller == Cancel

Crash == /\ pc["crasher"] = "Crash"
         /\ up /\ crashes < MaxCrash
         /\ up' = FALSE
         /\ epoch' = epoch + 1
         /\ crashes' = crashes + 1
         /\ pc' = [pc EXCEPT !["crasher"] = "Crash"]
         /\ UNCHANGED << valid, disk, branch, runtimeFile, fails, thirdWrites, 
                         cancels, saves, broken, status, content, base, saved, 
                         liveBytes, revOK, kind, prState, fate, contains, 
                         prBytes, ep, snap, prev, stageHead, checked, 
                         cancelled, chk, chkFile, fresh, same, seen, dirty, 
                         liveEdited, staleEnd, badOverwrite, raced, lostEdit, 
                         falseLive >>

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
                      /\ fresh' = {}
                 ELSE /\ TRUE
                      /\ UNCHANGED << status, fresh >>
           /\ pc' = [pc EXCEPT !["restarter"] = "Restart"]
           /\ UNCHANGED << valid, disk, branch, epoch, crashes, fails, 
                           thirdWrites, cancels, saves, content, base, saved, 
                           liveBytes, revOK, kind, prState, fate, contains, 
                           prBytes, ep, snap, prev, stageHead, checked, 
                           cancelled, chk, chkFile, same, seen, dirty, 
                           liveEdited, staleEnd, badOverwrite, raced, lostEdit, 
                           falseLive >>

restarter == Restart

Next == gh \/ opener \/ poller \/ coeditor \/ stopper \/ patient \/ third
           \/ canceller \/ crasher \/ restarter
           \/ (\E self \in Users: user(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Users : WF_vars(user(self))
        /\ WF_vars(gh)
        /\ WF_vars(poller)
        /\ WF_vars(patient)
        /\ WF_vars(restarter)

\* END TRANSLATION
=============================================================================
