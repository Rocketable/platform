---------------------- MODULE ExecuteBackgroundDelivery ----------------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Jobs, Outcomes, Modes, MaxRestarts, MaxStops, Expired,
          WakeMerges, LateMoveListed, NoteRender, DedupOnResume,
          RetainUnderTurn, StrictPhrase, PhraseByStore, AllowDelete

TextOf(o, store) == [kind |-> o, phrase |-> IF o # "bigtext" THEN "none" ELSE IF PhraseByStore THEN store ELSE "fetch the rest"]
FilesOf(j, o) == IF o = "file" THEN {j} ELSE {}
Fetchable(o) == o \in {"held", "bigtext"}
Stores(o) == Fetchable(o) \/ o = "file"
FailedOutcome(o) == o \in {"failure", "crash"}
Status(o) == IF FailedOutcome(o) THEN "failed" ELSE "completed"
KilledText == [kind |-> "aborted", phrase |-> "none"]
NoText == [kind |-> "none", phrase |-> "none"]
NoBody == [text |-> NoText, files |-> {}, degraded |-> FALSE]
NoFetch == [job |-> "none", store |-> "none", res |-> "none", expired |-> FALSE, turn |-> "none"]

(* --algorithm ExecuteBackgroundDelivery
variables
  mode \in [Jobs -> Modes],
  cph = [j \in Jobs |-> "dispatch"],
  wph = [j \in Jobs |-> "idle"],
  settled = [j \in Jobs |-> FALSE],
  detached = [j \in Jobs |-> FALSE],
  inReg = [j \in Jobs |-> FALSE],
  attachedReg = [j \in Jobs |-> FALSE],
  moved = [j \in Jobs |-> FALSE],
  wasMoved = [j \in Jobs |-> FALSE],
  recorded = [j \in Jobs |-> FALSE],
  endedAttached = [j \in Jobs |-> FALSE],
  outcome = [j \in Jobs |-> "none"],
  rendered = [j \in Jobs |-> "none"],
  resultBody = [j \in Jobs |-> NoBody],
  row = [j \in Jobs |-> "none"],
  note = [j \in Jobs |-> "none"],
  wake = [j \in Jobs |-> FALSE],
  claim = [j \in Jobs |-> FALSE],
  retained = [j \in Jobs |-> FALSE],
  wasRetained = [j \in Jobs |-> FALSE],
  age = [j \in Jobs |-> 0],
  fgSpill = [j \in Jobs |-> FALSE],
  callOut = [j \in Jobs |-> "none"],
  fgBody = [j \in Jobs |-> NoBody],
  noteCount = [j \in Jobs |-> 0],
  noteBody = [j \in Jobs |-> NoBody],
  via = [j \in Jobs |-> "none"],
  finalStatus = [j \in Jobs |-> "none"],
  noticeQueued = {},
  noticed = {},
  turn = "origin",
  applied = {},
  restarts = 0,
  stops = 0,
  lastFetch = NoFetch,
  convDeleted = FALSE;

define
  NoteText(j) ==
    IF row[j] = "killed"
    THEN IF NoteRender = "shared" THEN KilledText ELSE [kind |-> "background job killed", phrase |-> "none"]
    ELSE IF NoteRender = "separate" /\ FailedOutcome(outcome[j])
         THEN [kind |-> "job failed", phrase |-> "none"]
         ELSE resultBody[j].text
  NoteHasFiles(j) == row[j] = "completed" /\ resultBody[j].files # {}
  NoteFilesLoad(j) == retained[j] /\ age[j] < Expired
  NoteBodyOf(j) ==
    [text |-> NoteText(j),
     files |-> IF NoteHasFiles(j) /\ NoteFilesLoad(j) THEN resultBody[j].files ELSE {},
     degraded |-> NoteHasFiles(j) /\ ~NoteFilesLoad(j)]
  Deliverable == {j \in Jobs : note[j] = "pending" /\ j \notin applied}
  StepBoundary == turn = "origin" => \A j \in Jobs : cph[j] = "returned"
  Wakeable == {j \in Jobs : note[j] = "pending" /\ ~claim[j] /\ wake[j]}
  ModelHasResultID(j) ==
    rendered[j] # "none" /\ Fetchable(outcome[j]) /\
    (callOut[j] = "result" \/ (noteCount[j] >= 1 /\ row[j] \in {"completed", "failed"}))
  FetchResult(j) ==
    IF rendered[j] = "fg"
    THEN IF fgSpill[j] THEN "file" ELSE "unknown"
    ELSE IF retained[j] THEN IF age[j] < Expired THEN "file" ELSE "expired" ELSE "unknown"
end define;

macro EndTurn() begin
  note := [j \in Jobs |-> IF claim[j] /\ note[j] = "pending" THEN "consumed" ELSE note[j]];
  claim := [j \in Jobs |-> FALSE];
  turn := "none";
  applied := {};
  noticeQueued := {};
  fgSpill := [j \in Jobs |-> FALSE];
  if RetainUnderTurn then
    retained := [j \in Jobs |-> FALSE];
  end if;
end macro;

fair process Job \in Jobs
begin
JobStep:
  while TRUE do
    await ~convDeleted;
    either
      await cph[self] = "dispatch" /\ mode[self] = "bg" /\ turn = "origin";
      if row[self] = "none" then
        row[self] := "running";
        detached[self] := TRUE;
        inReg[self] := TRUE;
        recorded[self] := TRUE;
        wph[self] := "run";
      end if;
      callOut[self] := "placeholder";
      cph[self] := "returned";
    or
      await cph[self] = "dispatch" /\ mode[self] = "fg" /\ turn = "origin";
      if row[self] = "none" then
        inReg[self] := TRUE;
        attachedReg[self] := TRUE;
        wph[self] := "run";
        cph[self] := "wait";
      else
        callOut[self] := "placeholder";
        cph[self] := "returned";
      end if;
    or
      await cph[self] = "wait" /\ wph[self] = "finished";
      callOut[self] := "result";
      fgBody[self] := resultBody[self];
      finalStatus[self] := Status(outcome[self]);
      cph[self] := "returned";
    or
      await cph[self] = "wait" /\ moved[self];
      moved[self] := FALSE;
      if detached[self] \/ ~settled[self] then
        detached[self] := TRUE;
        cph[self] := "detaching";
      else
        recorded[self] := TRUE;
        cph[self] := "waitdone";
      end if;
    or
      await cph[self] = "detaching";
      row[self] := "running";
      recorded[self] := TRUE;
      callOut[self] := "placeholder";
      cph[self] := "returned";
    or
      await cph[self] = "waitdone" /\ wph[self] = "finished";
      callOut[self] := "result";
      fgBody[self] := resultBody[self];
      finalStatus[self] := Status(outcome[self]);
      cph[self] := "returned";
    or
      await wph[self] = "run";
      with o \in Outcomes do
        outcome[self] := o;
        if o = "crash" then
          resultBody[self] := [text |-> TextOf("crash", "none"), files |-> {}, degraded |-> FALSE];
          wph[self] := "rendered";
        else
          settled[self] := TRUE;
          wph[self] := "settled";
        end if;
      end with;
    or
      await wph[self] = "settled";
      with store = IF detached[self] THEN "7 days" ELSE "turn end" do
        if detached[self] then
          if Stores(outcome[self]) then
            retained[self] := TRUE;
            wasRetained[self] := TRUE;
            age[self] := 0;
          end if;
        elsif Fetchable(outcome[self]) then
          fgSpill[self] := TRUE;
        end if;
        rendered[self] := IF detached[self] THEN "bg" ELSE "fg";
        resultBody[self] := [text |-> TextOf(outcome[self], store), files |-> FilesOf(self, outcome[self]), degraded |-> FALSE];
        wph[self] := "rendered";
      end with;
    or
      await wph[self] = "rendered";
      inReg[self] := FALSE;
      endedAttached[self] := attachedReg[self];
      wph[self] := IF attachedReg[self] THEN "finished" ELSE "done";
    or
      await wph[self] = "done" /\ recorded[self];
      if row[self] = "running" then
        row[self] := Status(outcome[self]);
        note[self] := "pending";
        wake[self] := TRUE;
        finalStatus[self] := Status(outcome[self]);
      end if;
      wph[self] := "finished";
    end either;
  end while;
end process;

fair process Delivery = "delivery"
begin
DeliveryStep:
  while TRUE do
    await ~convDeleted;
    either
      await turn # "none" /\ StepBoundary /\ (Deliverable # {} \/ noticeQueued # {});
      with S = Deliverable do
        noticed := noticed \cup noticeQueued;
        noticeQueued := {};
        noteBody := [j \in Jobs |-> IF j \in S THEN NoteBodyOf(j) ELSE noteBody[j]];
        noteCount := [j \in Jobs |-> IF j \in S THEN noteCount[j] + 1 ELSE noteCount[j]];
        retained := [j \in Jobs |-> IF j \in S /\ NoteHasFiles(j) /\ age[j] = Expired THEN FALSE ELSE retained[j]];
        via := [j \in Jobs |-> IF j \in S THEN "drain" ELSE via[j]];
        claim := [j \in Jobs |-> claim[j] \/ j \in S];
        applied := applied \cup S;
      end with;
    or
      await turn = "none" /\ Wakeable # {};
      with S = {j \in Jobs : note[j] = "pending" /\ ~claim[j]},
           merged = UNION {NoteBodyOf(k).files : k \in S} do
        noteBody := [j \in Jobs |-> IF j \in S
                                    THEN IF WakeMerges THEN [NoteBodyOf(j) EXCEPT !.files = merged] ELSE NoteBodyOf(j)
                                    ELSE noteBody[j]];
        noteCount := [j \in Jobs |-> IF j \in S THEN noteCount[j] + 1 ELSE noteCount[j]];
        retained := [j \in Jobs |-> IF j \in S /\ NoteHasFiles(j) /\ age[j] = Expired THEN FALSE ELSE retained[j]];
        via := [j \in Jobs |-> IF j \in S THEN "wake" ELSE via[j]];
        claim := [j \in Jobs |-> claim[j] \/ j \in S];
        applied := S;
        turn := "wake";
      end with;
    or
      await turn = "none";
      turn := "user";
      applied := {};
    or
      await turn # "none" /\ StepBoundary /\ Deliverable = {} /\ noticeQueued = {};
      EndTurn();
    end either;
  end while;
end process;

process Env = "env"
begin
EnvStep:
  while TRUE do
    await ~convDeleted;
    either
      await turn = "origin" /\ \E j \in Jobs : inReg[j] /\ attachedReg[j];
      with M = {j \in Jobs : inReg[j] /\ attachedReg[j]},
           D = IF LateMoveListed THEN detached ELSE [j \in Jobs |-> detached[j] \/ (j \in M /\ ~settled[j])] do
        attachedReg := [j \in Jobs |-> attachedReg[j] /\ j \notin M];
        moved := [j \in Jobs |-> moved[j] \/ j \in M];
        wasMoved := [j \in Jobs |-> wasMoved[j] \/ j \in M];
        detached := D;
        noticeQueued := noticeQueued \cup (IF LateMoveListed THEN M ELSE {j \in M : D[j]});
      end with;
    or
      await restarts < MaxRestarts;
      with K = {j \in Jobs : row[j] = "running"},
           R = {j \in Jobs : cph[j] # "returned" /\ row[j] = "none"},
           P = {j \in Jobs : cph[j] # "returned"} do
        restarts := restarts + 1;
        noticeQueued := {};
        row := [j \in Jobs |-> IF j \in K THEN "killed" ELSE row[j]];
        note := [j \in Jobs |-> IF j \in K THEN "pending" ELSE note[j]];
        wake := [j \in Jobs |-> IF j \in K THEN FALSE ELSE wake[j]];
        finalStatus := [j \in Jobs |-> IF j \in K THEN "killed" ELSE finalStatus[j]];
        cph := [j \in Jobs |-> IF j \in P THEN "dispatch" ELSE cph[j]];
        wph := [j \in Jobs |-> IF j \in K \cup R THEN "gone" ELSE wph[j]];
        settled := [j \in Jobs |-> IF j \in R THEN FALSE ELSE settled[j]];
        detached := [j \in Jobs |-> IF j \in R THEN FALSE ELSE detached[j]];
        inReg := [j \in Jobs |-> FALSE];
        attachedReg := [j \in Jobs |-> FALSE];
        moved := [j \in Jobs |-> FALSE];
        recorded := [j \in Jobs |-> IF j \in R THEN FALSE ELSE recorded[j]];
        outcome := [j \in Jobs |-> IF j \in R THEN "none" ELSE outcome[j]];
        rendered := [j \in Jobs |-> IF j \in R THEN "none" ELSE rendered[j]];
        if ~DedupOnResume then
          applied := {};
        end if;
      end with;
    or
      await \E j \in Jobs : retained[j] /\ age[j] < Expired;
      age := [j \in Jobs |-> IF retained[j] /\ age[j] < Expired THEN age[j] + 1 ELSE age[j]];
    or
      await \E j \in Jobs : retained[j] /\ age[j] = Expired;
      retained := [j \in Jobs |-> retained[j] /\ age[j] < Expired];
    or
      await turn # "none";
      with j \in {k \in Jobs : ModelHasResultID(k)} do
        lastFetch := [job |-> j, store |-> rendered[j], res |-> FetchResult(j),
                      expired |-> rendered[j] = "bg" /\ age[j] = Expired, turn |-> turn];
        if FetchResult(j) = "expired" then
          retained[j] := FALSE;
        end if;
      end with;
    or
      await stops < MaxStops /\ turn # "none" /\ \A j \in Jobs : cph[j] \in {"dispatch", "wait", "returned"} /\ ~moved[j];
      stops := stops + 1;
      with A = {j \in Jobs : cph[j] \in {"dispatch", "wait"}} do
        callOut := [j \in Jobs |-> IF j \in A THEN "aborted" ELSE callOut[j]];
        cph := [j \in Jobs |-> IF j \in A THEN "returned" ELSE cph[j]];
        wph := [j \in Jobs |-> IF j \in A THEN "gone" ELSE wph[j]];
        inReg := [j \in Jobs |-> IF j \in A THEN FALSE ELSE inReg[j]];
        attachedReg := [j \in Jobs |-> IF j \in A THEN FALSE ELSE attachedReg[j]];
        finalStatus := [j \in Jobs |-> IF j \in A THEN "aborted" ELSE finalStatus[j]];
      end with;
      EndTurn();
    or
      await AllowDelete;
      retained := [j \in Jobs |-> FALSE];
      convDeleted := TRUE;
    end either;
  end while;
end process;
end algorithm; *)

\* BEGIN TRANSLATION
VARIABLES mode, cph, wph, settled, detached, inReg, attachedReg, moved, 
          wasMoved, recorded, endedAttached, outcome, rendered, resultBody, 
          row, note, wake, claim, retained, wasRetained, age, fgSpill, 
          callOut, fgBody, noteCount, noteBody, via, finalStatus, 
          noticeQueued, noticed, turn, applied, restarts, stops, lastFetch, 
          convDeleted

(* define statement *)
NoteText(j) ==
  IF row[j] = "killed"
  THEN IF NoteRender = "shared" THEN KilledText ELSE [kind |-> "background job killed", phrase |-> "none"]
  ELSE IF NoteRender = "separate" /\ FailedOutcome(outcome[j])
       THEN [kind |-> "job failed", phrase |-> "none"]
       ELSE resultBody[j].text
NoteHasFiles(j) == row[j] = "completed" /\ resultBody[j].files # {}
NoteFilesLoad(j) == retained[j] /\ age[j] < Expired
NoteBodyOf(j) ==
  [text |-> NoteText(j),
   files |-> IF NoteHasFiles(j) /\ NoteFilesLoad(j) THEN resultBody[j].files ELSE {},
   degraded |-> NoteHasFiles(j) /\ ~NoteFilesLoad(j)]
Deliverable == {j \in Jobs : note[j] = "pending" /\ j \notin applied}
StepBoundary == turn = "origin" => \A j \in Jobs : cph[j] = "returned"
Wakeable == {j \in Jobs : note[j] = "pending" /\ ~claim[j] /\ wake[j]}
ModelHasResultID(j) ==
  rendered[j] # "none" /\ Fetchable(outcome[j]) /\
  (callOut[j] = "result" \/ (noteCount[j] >= 1 /\ row[j] \in {"completed", "failed"}))
FetchResult(j) ==
  IF rendered[j] = "fg"
  THEN IF fgSpill[j] THEN "file" ELSE "unknown"
  ELSE IF retained[j] THEN IF age[j] < Expired THEN "file" ELSE "expired" ELSE "unknown"


vars == << mode, cph, wph, settled, detached, inReg, attachedReg, moved, 
           wasMoved, recorded, endedAttached, outcome, rendered, resultBody, 
           row, note, wake, claim, retained, wasRetained, age, fgSpill, 
           callOut, fgBody, noteCount, noteBody, via, finalStatus, 
           noticeQueued, noticed, turn, applied, restarts, stops, lastFetch, 
           convDeleted >>

ProcSet == (Jobs) \cup {"delivery"} \cup {"env"}

Init == (* Global variables *)
        /\ mode \in [Jobs -> Modes]
        /\ cph = [j \in Jobs |-> "dispatch"]
        /\ wph = [j \in Jobs |-> "idle"]
        /\ settled = [j \in Jobs |-> FALSE]
        /\ detached = [j \in Jobs |-> FALSE]
        /\ inReg = [j \in Jobs |-> FALSE]
        /\ attachedReg = [j \in Jobs |-> FALSE]
        /\ moved = [j \in Jobs |-> FALSE]
        /\ wasMoved = [j \in Jobs |-> FALSE]
        /\ recorded = [j \in Jobs |-> FALSE]
        /\ endedAttached = [j \in Jobs |-> FALSE]
        /\ outcome = [j \in Jobs |-> "none"]
        /\ rendered = [j \in Jobs |-> "none"]
        /\ resultBody = [j \in Jobs |-> NoBody]
        /\ row = [j \in Jobs |-> "none"]
        /\ note = [j \in Jobs |-> "none"]
        /\ wake = [j \in Jobs |-> FALSE]
        /\ claim = [j \in Jobs |-> FALSE]
        /\ retained = [j \in Jobs |-> FALSE]
        /\ wasRetained = [j \in Jobs |-> FALSE]
        /\ age = [j \in Jobs |-> 0]
        /\ fgSpill = [j \in Jobs |-> FALSE]
        /\ callOut = [j \in Jobs |-> "none"]
        /\ fgBody = [j \in Jobs |-> NoBody]
        /\ noteCount = [j \in Jobs |-> 0]
        /\ noteBody = [j \in Jobs |-> NoBody]
        /\ via = [j \in Jobs |-> "none"]
        /\ finalStatus = [j \in Jobs |-> "none"]
        /\ noticeQueued = {}
        /\ noticed = {}
        /\ turn = "origin"
        /\ applied = {}
        /\ restarts = 0
        /\ stops = 0
        /\ lastFetch = NoFetch
        /\ convDeleted = FALSE

Job(self) == /\ ~convDeleted
             /\ \/ /\ cph[self] = "dispatch" /\ mode[self] = "bg" /\ turn = "origin"
                   /\ IF row[self] = "none"
                         THEN /\ row' = [row EXCEPT ![self] = "running"]
                              /\ detached' = [detached EXCEPT ![self] = TRUE]
                              /\ inReg' = [inReg EXCEPT ![self] = TRUE]
                              /\ recorded' = [recorded EXCEPT ![self] = TRUE]
                              /\ wph' = [wph EXCEPT ![self] = "run"]
                         ELSE /\ TRUE
                              /\ UNCHANGED << wph, detached, inReg, recorded, 
                                              row >>
                   /\ callOut' = [callOut EXCEPT ![self] = "placeholder"]
                   /\ cph' = [cph EXCEPT ![self] = "returned"]
                   /\ UNCHANGED <<settled, attachedReg, moved, endedAttached, outcome, rendered, resultBody, note, wake, retained, wasRetained, age, fgSpill, fgBody, finalStatus>>
                \/ /\ cph[self] = "dispatch" /\ mode[self] = "fg" /\ turn = "origin"
                   /\ IF row[self] = "none"
                         THEN /\ inReg' = [inReg EXCEPT ![self] = TRUE]
                              /\ attachedReg' = [attachedReg EXCEPT ![self] = TRUE]
                              /\ wph' = [wph EXCEPT ![self] = "run"]
                              /\ cph' = [cph EXCEPT ![self] = "wait"]
                              /\ UNCHANGED callOut
                         ELSE /\ callOut' = [callOut EXCEPT ![self] = "placeholder"]
                              /\ cph' = [cph EXCEPT ![self] = "returned"]
                              /\ UNCHANGED << wph, inReg, attachedReg >>
                   /\ UNCHANGED <<settled, detached, moved, recorded, endedAttached, outcome, rendered, resultBody, row, note, wake, retained, wasRetained, age, fgSpill, fgBody, finalStatus>>
                \/ /\ cph[self] = "wait" /\ wph[self] = "finished"
                   /\ callOut' = [callOut EXCEPT ![self] = "result"]
                   /\ fgBody' = [fgBody EXCEPT ![self] = resultBody[self]]
                   /\ finalStatus' = [finalStatus EXCEPT ![self] = Status(outcome[self])]
                   /\ cph' = [cph EXCEPT ![self] = "returned"]
                   /\ UNCHANGED <<wph, settled, detached, inReg, attachedReg, moved, recorded, endedAttached, outcome, rendered, resultBody, row, note, wake, retained, wasRetained, age, fgSpill>>
                \/ /\ cph[self] = "wait" /\ moved[self]
                   /\ moved' = [moved EXCEPT ![self] = FALSE]
                   /\ IF detached[self] \/ ~settled[self]
                         THEN /\ detached' = [detached EXCEPT ![self] = TRUE]
                              /\ cph' = [cph EXCEPT ![self] = "detaching"]
                              /\ UNCHANGED recorded
                         ELSE /\ recorded' = [recorded EXCEPT ![self] = TRUE]
                              /\ cph' = [cph EXCEPT ![self] = "waitdone"]
                              /\ UNCHANGED detached
                   /\ UNCHANGED <<wph, settled, inReg, attachedReg, endedAttached, outcome, rendered, resultBody, row, note, wake, retained, wasRetained, age, fgSpill, callOut, fgBody, finalStatus>>
                \/ /\ cph[self] = "detaching"
                   /\ row' = [row EXCEPT ![self] = "running"]
                   /\ recorded' = [recorded EXCEPT ![self] = TRUE]
                   /\ callOut' = [callOut EXCEPT ![self] = "placeholder"]
                   /\ cph' = [cph EXCEPT ![self] = "returned"]
                   /\ UNCHANGED <<wph, settled, detached, inReg, attachedReg, moved, endedAttached, outcome, rendered, resultBody, note, wake, retained, wasRetained, age, fgSpill, fgBody, finalStatus>>
                \/ /\ cph[self] = "waitdone" /\ wph[self] = "finished"
                   /\ callOut' = [callOut EXCEPT ![self] = "result"]
                   /\ fgBody' = [fgBody EXCEPT ![self] = resultBody[self]]
                   /\ finalStatus' = [finalStatus EXCEPT ![self] = Status(outcome[self])]
                   /\ cph' = [cph EXCEPT ![self] = "returned"]
                   /\ UNCHANGED <<wph, settled, detached, inReg, attachedReg, moved, recorded, endedAttached, outcome, rendered, resultBody, row, note, wake, retained, wasRetained, age, fgSpill>>
                \/ /\ wph[self] = "run"
                   /\ \E o \in Outcomes:
                        /\ outcome' = [outcome EXCEPT ![self] = o]
                        /\ IF o = "crash"
                              THEN /\ resultBody' = [resultBody EXCEPT ![self] = [text |-> TextOf("crash", "none"), files |-> {}, degraded |-> FALSE]]
                                   /\ wph' = [wph EXCEPT ![self] = "rendered"]
                                   /\ UNCHANGED settled
                              ELSE /\ settled' = [settled EXCEPT ![self] = TRUE]
                                   /\ wph' = [wph EXCEPT ![self] = "settled"]
                                   /\ UNCHANGED resultBody
                   /\ UNCHANGED <<cph, detached, inReg, attachedReg, moved, recorded, endedAttached, rendered, row, note, wake, retained, wasRetained, age, fgSpill, callOut, fgBody, finalStatus>>
                \/ /\ wph[self] = "settled"
                   /\ LET store == IF detached[self] THEN "7 days" ELSE "turn end" IN
                        /\ IF detached[self]
                              THEN /\ IF Stores(outcome[self])
                                         THEN /\ retained' = [retained EXCEPT ![self] = TRUE]
                                              /\ wasRetained' = [wasRetained EXCEPT ![self] = TRUE]
                                              /\ age' = [age EXCEPT ![self] = 0]
                                         ELSE /\ TRUE
                                              /\ UNCHANGED << retained, 
                                                              wasRetained, age >>
                                   /\ UNCHANGED fgSpill
                              ELSE /\ IF Fetchable(outcome[self])
                                         THEN /\ fgSpill' = [fgSpill EXCEPT ![self] = TRUE]
                                         ELSE /\ TRUE
                                              /\ UNCHANGED fgSpill
                                   /\ UNCHANGED << retained, wasRetained, age >>
                        /\ rendered' = [rendered EXCEPT ![self] = IF detached[self] THEN "bg" ELSE "fg"]
                        /\ resultBody' = [resultBody EXCEPT ![self] = [text |-> TextOf(outcome[self], store), files |-> FilesOf(self, outcome[self]), degraded |-> FALSE]]
                        /\ wph' = [wph EXCEPT ![self] = "rendered"]
                   /\ UNCHANGED <<cph, settled, detached, inReg, attachedReg, moved, recorded, endedAttached, outcome, row, note, wake, callOut, fgBody, finalStatus>>
                \/ /\ wph[self] = "rendered"
                   /\ inReg' = [inReg EXCEPT ![self] = FALSE]
                   /\ endedAttached' = [endedAttached EXCEPT ![self] = attachedReg[self]]
                   /\ wph' = [wph EXCEPT ![self] = IF attachedReg[self] THEN "finished" ELSE "done"]
                   /\ UNCHANGED <<cph, settled, detached, attachedReg, moved, recorded, outcome, rendered, resultBody, row, note, wake, retained, wasRetained, age, fgSpill, callOut, fgBody, finalStatus>>
                \/ /\ wph[self] = "done" /\ recorded[self]
                   /\ IF row[self] = "running"
                         THEN /\ row' = [row EXCEPT ![self] = Status(outcome[self])]
                              /\ note' = [note EXCEPT ![self] = "pending"]
                              /\ wake' = [wake EXCEPT ![self] = TRUE]
                              /\ finalStatus' = [finalStatus EXCEPT ![self] = Status(outcome[self])]
                         ELSE /\ TRUE
                              /\ UNCHANGED << row, note, wake, finalStatus >>
                   /\ wph' = [wph EXCEPT ![self] = "finished"]
                   /\ UNCHANGED <<cph, settled, detached, inReg, attachedReg, moved, recorded, endedAttached, outcome, rendered, resultBody, retained, wasRetained, age, fgSpill, callOut, fgBody>>
             /\ UNCHANGED << mode, wasMoved, claim, noteCount, noteBody, via, 
                             noticeQueued, noticed, turn, applied, restarts, 
                             stops, lastFetch, convDeleted >>

Delivery == /\ ~convDeleted
            /\ \/ /\ turn # "none" /\ StepBoundary /\ (Deliverable # {} \/ noticeQueued # {})
                  /\ LET S == Deliverable IN
                       /\ noticed' = (noticed \cup noticeQueued)
                       /\ noticeQueued' = {}
                       /\ noteBody' = [j \in Jobs |-> IF j \in S THEN NoteBodyOf(j) ELSE noteBody[j]]
                       /\ noteCount' = [j \in Jobs |-> IF j \in S THEN noteCount[j] + 1 ELSE noteCount[j]]
                       /\ retained' = [j \in Jobs |-> IF j \in S /\ NoteHasFiles(j) /\ age[j] = Expired THEN FALSE ELSE retained[j]]
                       /\ via' = [j \in Jobs |-> IF j \in S THEN "drain" ELSE via[j]]
                       /\ claim' = [j \in Jobs |-> claim[j] \/ j \in S]
                       /\ applied' = (applied \cup S)
                  /\ UNCHANGED <<note, fgSpill, turn>>
               \/ /\ turn = "none" /\ Wakeable # {}
                  /\ LET S == {j \in Jobs : note[j] = "pending" /\ ~claim[j]} IN
                       LET merged == UNION {NoteBodyOf(k).files : k \in S} IN
                         /\ noteBody' = [j \in Jobs |-> IF j \in S
                                                        THEN IF WakeMerges THEN [NoteBodyOf(j) EXCEPT !.files = merged] ELSE NoteBodyOf(j)
                                                        ELSE noteBody[j]]
                         /\ noteCount' = [j \in Jobs |-> IF j \in S THEN noteCount[j] + 1 ELSE noteCount[j]]
                         /\ retained' = [j \in Jobs |-> IF j \in S /\ NoteHasFiles(j) /\ age[j] = Expired THEN FALSE ELSE retained[j]]
                         /\ via' = [j \in Jobs |-> IF j \in S THEN "wake" ELSE via[j]]
                         /\ claim' = [j \in Jobs |-> claim[j] \/ j \in S]
                         /\ applied' = S
                         /\ turn' = "wake"
                  /\ UNCHANGED <<note, fgSpill, noticeQueued, noticed>>
               \/ /\ turn = "none"
                  /\ turn' = "user"
                  /\ applied' = {}
                  /\ UNCHANGED <<note, claim, retained, fgSpill, noteCount, noteBody, via, noticeQueued, noticed>>
               \/ /\ turn # "none" /\ StepBoundary /\ Deliverable = {} /\ noticeQueued = {}
                  /\ note' = [j \in Jobs |-> IF claim[j] /\ note[j] = "pending" THEN "consumed" ELSE note[j]]
                  /\ claim' = [j \in Jobs |-> FALSE]
                  /\ turn' = "none"
                  /\ applied' = {}
                  /\ noticeQueued' = {}
                  /\ fgSpill' = [j \in Jobs |-> FALSE]
                  /\ IF RetainUnderTurn
                        THEN /\ retained' = [j \in Jobs |-> FALSE]
                        ELSE /\ TRUE
                             /\ UNCHANGED retained
                  /\ UNCHANGED <<noteCount, noteBody, via, noticed>>
            /\ UNCHANGED << mode, cph, wph, settled, detached, inReg, 
                            attachedReg, moved, wasMoved, recorded, 
                            endedAttached, outcome, rendered, resultBody, row, 
                            wake, wasRetained, age, callOut, fgBody, 
                            finalStatus, restarts, stops, lastFetch, 
                            convDeleted >>

Env == /\ ~convDeleted
       /\ \/ /\ turn = "origin" /\ \E j \in Jobs : inReg[j] /\ attachedReg[j]
             /\ LET M == {j \in Jobs : inReg[j] /\ attachedReg[j]} IN
                  LET D == IF LateMoveListed THEN detached ELSE [j \in Jobs |-> detached[j] \/ (j \in M /\ ~settled[j])] IN
                    /\ attachedReg' = [j \in Jobs |-> attachedReg[j] /\ j \notin M]
                    /\ moved' = [j \in Jobs |-> moved[j] \/ j \in M]
                    /\ wasMoved' = [j \in Jobs |-> wasMoved[j] \/ j \in M]
                    /\ detached' = D
                    /\ noticeQueued' = (noticeQueued \cup (IF LateMoveListed THEN M ELSE {j \in M : D[j]}))
             /\ UNCHANGED <<cph, wph, settled, inReg, recorded, outcome, rendered, row, note, wake, claim, retained, age, fgSpill, callOut, finalStatus, turn, applied, restarts, stops, lastFetch, convDeleted>>
          \/ /\ restarts < MaxRestarts
             /\ LET K == {j \in Jobs : row[j] = "running"} IN
                  LET R == {j \in Jobs : cph[j] # "returned" /\ row[j] = "none"} IN
                    LET P == {j \in Jobs : cph[j] # "returned"} IN
                      /\ restarts' = restarts + 1
                      /\ noticeQueued' = {}
                      /\ row' = [j \in Jobs |-> IF j \in K THEN "killed" ELSE row[j]]
                      /\ note' = [j \in Jobs |-> IF j \in K THEN "pending" ELSE note[j]]
                      /\ wake' = [j \in Jobs |-> IF j \in K THEN FALSE ELSE wake[j]]
                      /\ finalStatus' = [j \in Jobs |-> IF j \in K THEN "killed" ELSE finalStatus[j]]
                      /\ cph' = [j \in Jobs |-> IF j \in P THEN "dispatch" ELSE cph[j]]
                      /\ wph' = [j \in Jobs |-> IF j \in K \cup R THEN "gone" ELSE wph[j]]
                      /\ settled' = [j \in Jobs |-> IF j \in R THEN FALSE ELSE settled[j]]
                      /\ detached' = [j \in Jobs |-> IF j \in R THEN FALSE ELSE detached[j]]
                      /\ inReg' = [j \in Jobs |-> FALSE]
                      /\ attachedReg' = [j \in Jobs |-> FALSE]
                      /\ moved' = [j \in Jobs |-> FALSE]
                      /\ recorded' = [j \in Jobs |-> IF j \in R THEN FALSE ELSE recorded[j]]
                      /\ outcome' = [j \in Jobs |-> IF j \in R THEN "none" ELSE outcome[j]]
                      /\ rendered' = [j \in Jobs |-> IF j \in R THEN "none" ELSE rendered[j]]
                      /\ IF ~DedupOnResume
                            THEN /\ applied' = {}
                            ELSE /\ TRUE
                                 /\ UNCHANGED applied
             /\ UNCHANGED <<wasMoved, claim, retained, age, fgSpill, callOut, turn, stops, lastFetch, convDeleted>>
          \/ /\ \E j \in Jobs : retained[j] /\ age[j] < Expired
             /\ age' = [j \in Jobs |-> IF retained[j] /\ age[j] < Expired THEN age[j] + 1 ELSE age[j]]
             /\ UNCHANGED <<cph, wph, settled, detached, inReg, attachedReg, moved, wasMoved, recorded, outcome, rendered, row, note, wake, claim, retained, fgSpill, callOut, finalStatus, noticeQueued, turn, applied, restarts, stops, lastFetch, convDeleted>>
          \/ /\ \E j \in Jobs : retained[j] /\ age[j] = Expired
             /\ retained' = [j \in Jobs |-> retained[j] /\ age[j] < Expired]
             /\ UNCHANGED <<cph, wph, settled, detached, inReg, attachedReg, moved, wasMoved, recorded, outcome, rendered, row, note, wake, claim, age, fgSpill, callOut, finalStatus, noticeQueued, turn, applied, restarts, stops, lastFetch, convDeleted>>
          \/ /\ turn # "none"
             /\ \E j \in {k \in Jobs : ModelHasResultID(k)}:
                  /\ lastFetch' = [job |-> j, store |-> rendered[j], res |-> FetchResult(j),
                                   expired |-> rendered[j] = "bg" /\ age[j] = Expired, turn |-> turn]
                  /\ IF FetchResult(j) = "expired"
                        THEN /\ retained' = [retained EXCEPT ![j] = FALSE]
                        ELSE /\ TRUE
                             /\ UNCHANGED retained
             /\ UNCHANGED <<cph, wph, settled, detached, inReg, attachedReg, moved, wasMoved, recorded, outcome, rendered, row, note, wake, claim, age, fgSpill, callOut, finalStatus, noticeQueued, turn, applied, restarts, stops, convDeleted>>
          \/ /\ stops < MaxStops /\ turn # "none" /\ \A j \in Jobs : cph[j] \in {"dispatch", "wait", "returned"} /\ ~moved[j]
             /\ stops' = stops + 1
             /\ LET A == {j \in Jobs : cph[j] \in {"dispatch", "wait"}} IN
                  /\ callOut' = [j \in Jobs |-> IF j \in A THEN "aborted" ELSE callOut[j]]
                  /\ cph' = [j \in Jobs |-> IF j \in A THEN "returned" ELSE cph[j]]
                  /\ wph' = [j \in Jobs |-> IF j \in A THEN "gone" ELSE wph[j]]
                  /\ inReg' = [j \in Jobs |-> IF j \in A THEN FALSE ELSE inReg[j]]
                  /\ attachedReg' = [j \in Jobs |-> IF j \in A THEN FALSE ELSE attachedReg[j]]
                  /\ finalStatus' = [j \in Jobs |-> IF j \in A THEN "aborted" ELSE finalStatus[j]]
             /\ note' = [j \in Jobs |-> IF claim[j] /\ note[j] = "pending" THEN "consumed" ELSE note[j]]
             /\ claim' = [j \in Jobs |-> FALSE]
             /\ turn' = "none"
             /\ applied' = {}
             /\ noticeQueued' = {}
             /\ fgSpill' = [j \in Jobs |-> FALSE]
             /\ IF RetainUnderTurn
                   THEN /\ retained' = [j \in Jobs |-> FALSE]
                   ELSE /\ TRUE
                        /\ UNCHANGED retained
             /\ UNCHANGED <<settled, detached, moved, wasMoved, recorded, outcome, rendered, row, wake, age, restarts, lastFetch, convDeleted>>
          \/ /\ AllowDelete
             /\ retained' = [j \in Jobs |-> FALSE]
             /\ convDeleted' = TRUE
             /\ UNCHANGED <<cph, wph, settled, detached, inReg, attachedReg, moved, wasMoved, recorded, outcome, rendered, row, note, wake, claim, age, fgSpill, callOut, finalStatus, noticeQueued, turn, applied, restarts, stops, lastFetch>>
       /\ UNCHANGED << mode, endedAttached, resultBody, wasRetained, fgBody, 
                       noteCount, noteBody, via, noticed >>

Next == Delivery \/ Env
           \/ (\E self \in Jobs: Job(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Jobs : WF_vars(Job(self))
        /\ WF_vars(Delivery)

\* END TRANSLATION



TypeOK ==
  /\ cph \in [Jobs -> {"dispatch", "wait", "detaching", "waitdone", "returned"}]
  /\ wph \in [Jobs -> {"idle", "run", "settled", "rendered", "done", "finished", "gone"}]
  /\ row \in [Jobs -> {"none", "running", "completed", "failed", "killed"}]
  /\ note \in [Jobs -> {"none", "pending", "consumed"}]
  /\ callOut \in [Jobs -> {"none", "placeholder", "aborted", "result"}]
  /\ age \in [Jobs -> 0..Expired]
  /\ noteCount \in [Jobs -> 0..(MaxRestarts + 1)]
  /\ turn \in {"none", "origin", "wake", "user"}
  /\ applied \subseteq Jobs
  /\ noticed \subseteq Jobs
  /\ noticeQueued \subseteq Jobs

RefText(j) == TextOf(outcome[j], "turn end")
TextMatches(t, j) == IF StrictPhrase THEN t = RefText(j) ELSE t.kind = RefText(j).kind
IsResultNote(j) == noteCount[j] >= 1 /\ row[j] \in {"completed", "failed"}

SameText ==
  \A j \in Jobs :
    /\ IsResultNote(j) /\ ~noteBody[j].degraded => TextMatches(noteBody[j].text, j)
    /\ callOut[j] = "result" => TextMatches(fgBody[j].text, j)

NoteCarriesOwnFiles ==
  \A j \in Jobs :
    /\ IsResultNote(j) => noteBody[j].files = IF noteBody[j].degraded THEN {} ELSE FilesOf(j, outcome[j])
    /\ callOut[j] = "result" => fgBody[j].files = FilesOf(j, outcome[j])

KilledTextShared ==
  \A j \in Jobs : row[j] = "killed" /\ noteCount[j] >= 1 => noteBody[j].text = KilledText

ExactlyOnce ==
  \A j \in Jobs :
    /\ noteCount[j] <= 1
    /\ ~(callOut[j] = "result" /\ noteCount[j] >= 1)
    /\ noteCount[j] >= 1 => row[j] # "none"

StatusCPlus ==
  \A j \in Jobs :
    finalStatus[j] \in {"completed", "failed"} => (finalStatus[j] = "failed" <=> FailedOutcome(outcome[j]))

HeldFetchableUntilExpiry ==
  lastFetch.store = "bg" /\ ~lastFetch.expired => lastFetch.res = "file"

ExpiredFetchFails ==
  lastFetch.store = "bg" /\ lastFetch.expired => lastFetch.res \in {"expired", "unknown"}

RetainedDeletedOnlyBySweep ==
  \A j \in Jobs : wasRetained[j] /\ ~retained[j] => age[j] = Expired \/ convDeleted

MovedNoticeHonest ==
  \A j \in noticed : callOut[j] # "result"

NoteDelivered ==
  \A j \in Jobs : note[j] = "pending" ~> (note[j] = "consumed" \/ convDeleted)

PlaceholderGetsNote ==
  \A j \in Jobs : callOut[j] = "placeholder" ~> (noteCount[j] >= 1 \/ convDeleted)

NeverMovedNote == ~\E j \in Jobs : wasMoved[j] /\ IsResultNote(j)
NeverLateMoveForeground == ~\E j \in Jobs : wasMoved[j] /\ callOut[j] = "result"
NeverKilledNote == ~\E j \in Jobs : row[j] = "killed" /\ noteCount[j] >= 1
NeverWakeDelivery == ~\E j \in Jobs : via[j] = "wake" /\ IsResultNote(j) /\ noteBody[j].files # {}
NeverDrainDelivery == ~\E j \in Jobs : via[j] = "drain" /\ IsResultNote(j) /\ noteBody[j].files # {}
NeverLaterTurnFetch == ~(lastFetch.store = "bg" /\ lastFetch.res = "file" /\ lastFetch.turn \in {"wake", "user"})
NeverExpiredFetch == ~(lastFetch.store = "bg" /\ lastFetch.expired)
NeverDegradedNote == ~\E j \in Jobs : IsResultNote(j) /\ noteBody[j].degraded
NeverAbortedCall == ~\E j \in Jobs : callOut[j] = "aborted" /\ outcome[j] # "none" /\ row[j] = "none"
NeverForegroundExpiredID == ~(lastFetch.store = "fg" /\ lastFetch.res = "unknown")
=============================================================================
