------------------------- MODULE OpenCodeTitleCheck -------------------------
(* Who writes a remote OpenCode session's title, and in what order, once
   RocketClaw checks automatic titles for remote sessions
   (internal/rocketclaw/backend/opencode.go, store.go, bridge.go).

   One session row holds name, title, generated_name (gen) and synced_at. The
   OpenCode server holds the session's title (up). Titles are tokens numbered in
   the order they were made; kinds[t] says who made token t:
   "oc"    a title from OpenCode (its own titler or a rename made there),
   "typed" a name a person typed in RocketClaw,
   "gen"   a title RocketClaw's title check generated.
   gen = 0 is NULL. clock is the database clock: each now() or
   clock_timestamp() takes the next value. seq numbers the writes of OpenCode's
   title; upVer is the write OpenCode holds and nameVer the write the local
   name comes from (a typed name that never reached OpenCode counts as newer
   than OpenCode's title).

   typedT is the row's typed_titles: names typed in RocketClaw that were sent,
   or are being sent, to OpenCode and not yet seen back.

   Writers:
   - sync     the OpenCode sync loop: pushOpenCodeNames, then syncOpenCode
              (started := now(), list OpenCode's sessions, upsert in a
              transaction whose now() is txNow).
   - retitle  a background title check: reads the name without a lock, judges
              it, then NameGenerated: lock the row, send the new title to
              OpenCode, stamp synced_at, commit.
   - user     a rename typed in RocketClaw: UpdateConversationDetails, the same
              lock, send, stamp, commit order.
   - oc       a rename made on the OpenCode side, at any time.

   A transaction's writes become visible at its commit, and every writer of the
   row waits for the row lock first; readers without the lock see committed
   values only. A request to OpenCode can fail, and then its transaction rolls
   back.

   Design switches (all FALSE/TRUE as built, see the README):
   - JudgedGuard: NameGenerated only replaces the name the check judged.
   - StampFirst: NameGenerated stamps synced_at before sending to OpenCode.
   - GuardOff: NameGenerated skips its "name is generated" guard.
   - SyncAdoptsAlways: the sync marks every OpenCode title as generated, even
     when the title did not change.
   - TypedMarks: a name typed in RocketClaw is recorded in typed_titles, in its
     own commit, before it is sent, and the sync never marks a recorded name
     generated. A pushed name needs no record: it waits as name # title, and
     the sync keeps a waiting name and its mark.
   - PushLocks: pushOpenCodeNames locks the row, re-checks that the name is
     still the pending one, sends it and writes the title under that lock.
   - KeepPending: the sync keeps a name that is still waiting to be sent.
   - CommitFail: a transaction can fail at commit after OpenCode accepted.
   - LegacyPending: the row starts with a typed name that never reached
     OpenCode (name # title), the case pushOpenCodeNames exists for. A push is
     refused or fails at commit at most PushFailures times. *)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS Retitlers, Users, Renamers, MaxPasses, PushFailures,
          JudgedGuard, StampFirst, GuardOff, SyncAdoptsAlways, TypedMarks, PushLocks, KeepPending,
          CommitFail, LegacyPending

ASSUME MaxPasses \in Nat \ {0} /\ PushFailures \in Nat
ASSUME {JudgedGuard, StampFirst, GuardOff, SyncAdoptsAlways, TypedMarks, PushLocks, KeepPending,
        CommitFail, LegacyPending} \subseteq BOOLEAN

Writers == Retitlers \cup Users \cup Renamers

(* --algorithm OpenCodeTitleCheck
variables
    kinds = IF LegacyPending THEN <<"oc", "typed">> ELSE <<"oc">>,
    up = 1,
    name = IF LegacyPending THEN 2 ELSE 1,
    title = 1,
    gen = IF LegacyPending THEN 0 ELSE 1,
    syncedAt = 0,
    clock = 1,
    lock = "none",
    retitled = 0,
    keptLocal = FALSE,
    seq = IF LegacyPending THEN 2 ELSE 1,
    upVer = 1,
    nameVer = IF LegacyPending THEN 2 ELSE 1,
    typedT = {},
    markSaved = FALSE,
    pushFailures = PushFailures;

define
    Token == 1..Len(kinds)
    TypeOK ==
        /\ \A t \in Token : kinds[t] \in {"oc", "typed", "gen"}
        /\ up \in Token /\ name \in Token /\ title \in Token /\ gen \in 0..Len(kinds)
        /\ syncedAt \in Nat /\ clock \in Nat
        /\ upVer \in 1..seq /\ nameVer \in 1..seq
        /\ lock \in {"none", "sync"} \cup Retitlers \cup Users
        /\ typedT \subseteq Token

    WritersDone == \A w \in Writers : pc[w] = "Done"
    Pending == name # title
end define;

fair process sync = "sync"
variables started = 0, listed = 0, listedVer = 0, txNow = 0, passes = 0, finalPass = FALSE, finalDone = FALSE, pushName = 0;
begin
Loop:
    while ~(WritersDone /\ finalDone) do
        await passes < MaxPasses \/ WritersDone;
        finalPass := WritersDone;
        passes := passes + 1;
PushRead:
        pushName := IF Pending THEN name ELSE 0;
PushLock:
        if pushName # 0 /\ PushLocks then
            await lock = "none";
            if name = pushName /\ Pending then
                lock := "sync";
            else
                pushName := 0;
            end if;
        end if;
PushPatch:
        if pushName # 0 then
            either
                up := pushName;
                seq := seq + 1;
                upVer := seq;
            or
                await pushFailures > 0;
                pushFailures := pushFailures - 1;
                pushName := 0;
                if PushLocks then
                    lock := "none";
                end if;
            end either;
        end if;
PushUpdate:
        if pushName # 0 then
            if ~PushLocks then
                await lock = "none";
            end if;
            either
                title := name;
            or
                await CommitFail /\ pushFailures > 0;
                pushFailures := pushFailures - 1;
            end either;
            if PushLocks then
                lock := "none";
            end if;
        end if;
Started:
        started := clock;
        clock := clock + 1;
List:
        listed := up;
        listedVer := upVer;
TxBegin:
        txNow := clock;
        clock := clock + 1;
Upsert:
        await lock = "none";
        with keep = (syncedAt > started), keepName = (keep \/ (KeepPending /\ Pending)) do
            if keep /\ listed # name then
                keptLocal := TRUE;
            end if;
            if TypedMarks /\ ~keepName /\ listed \in typedT /\ listed # title then
                markSaved := TRUE;
            end if;
            gen := IF keepName \/ (~SyncAdoptsAlways /\ listed = title) \/ (TypedMarks /\ listed \in typedT) THEN gen ELSE listed;
            typedT := IF keep THEN typedT ELSE typedT \ {listed};
            title := IF keep THEN title ELSE listed;
            name := IF keepName THEN name ELSE listed;
            nameVer := IF keepName THEN nameVer ELSE listedVer;
            syncedAt := txNow;
        end with;
        finalDone := finalPass /\ name = title;
    end while;
end process;

fair process retitle \in Retitlers
variables judged = 0, fin = 0, stamp = 0, finVer = 0;
begin
Read:
    if name = gen then
        judged := name;
    else
        goto Done;
    end if;
Judge:
    either
        goto Done;
    or
        skip;
    end either;
Guard:
    await lock = "none";
    if (GuardOff \/ name = gen) /\ (~JudgedGuard \/ name = judged) then
        lock := self;
        fin := Len(kinds) + 1;
        kinds := Append(kinds, "gen");
        if StampFirst then
            stamp := clock;
            clock := clock + 1;
        end if;
    else
        goto Done;
    end if;
Patch:
    either
        up := fin;
        seq := seq + 1;
        upVer := seq;
        finVer := seq;
    or
        lock := "none";
        goto Done;
    end either;
Stamp:
    if ~StampFirst then
        stamp := clock;
        clock := clock + 1;
    end if;
Commit:
    either
        name := fin;
        nameVer := finVer;
        gen := fin;
        title := fin;
        syncedAt := stamp;
        retitled := retitled + 1;
        lock := "none";
    or
        await CommitFail;
        lock := "none";
    end either;
end process;

fair process user \in Users
variables typed = 0, ustamp = 0, typedVer = 0;
begin
UMark:
    typed := Len(kinds) + 1;
    kinds := Append(kinds, "typed");
    if TypedMarks then
        typedT := typedT \cup {typed};
    end if;
ULock:
    await lock = "none";
    lock := self;
UPatch:
    either
        up := typed;
        seq := seq + 1;
        upVer := seq;
        typedVer := seq;
    or
        lock := "none";
        goto Done;
    end either;
UStamp:
    ustamp := clock;
    clock := clock + 1;
UCommit:
    either
        name := typed;
        nameVer := typedVer;
        title := typed;
        typedT := typedT \ {typed};
        syncedAt := ustamp;
        lock := "none";
    or
        await CommitFail;
        lock := "none";
    end either;
end process;

fair process renamer \in Renamers
begin
Rename:
    up := Len(kinds) + 1;
    kinds := Append(kinds, "oc");
    seq := seq + 1;
    upVer := seq;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, kinds, up, name, title, gen, syncedAt, clock, lock, retitled, 
          keptLocal, seq, upVer, nameVer, typedT, markSaved, pushFailures

(* define statement *)
Token == 1..Len(kinds)
TypeOK ==
    /\ \A t \in Token : kinds[t] \in {"oc", "typed", "gen"}
    /\ up \in Token /\ name \in Token /\ title \in Token /\ gen \in 0..Len(kinds)
    /\ syncedAt \in Nat /\ clock \in Nat
    /\ upVer \in 1..seq /\ nameVer \in 1..seq
    /\ lock \in {"none", "sync"} \cup Retitlers \cup Users
    /\ typedT \subseteq Token

WritersDone == \A w \in Writers : pc[w] = "Done"
Pending == name # title

VARIABLES started, listed, listedVer, txNow, passes, finalPass, finalDone, 
          pushName, judged, fin, stamp, finVer, typed, ustamp, typedVer

vars == << pc, kinds, up, name, title, gen, syncedAt, clock, lock, retitled, 
           keptLocal, seq, upVer, nameVer, typedT, markSaved, pushFailures, 
           started, listed, listedVer, txNow, passes, finalPass, finalDone, 
           pushName, judged, fin, stamp, finVer, typed, ustamp, typedVer >>

ProcSet == {"sync"} \cup (Retitlers) \cup (Users) \cup (Renamers)

Init == (* Global variables *)
        /\ kinds = IF LegacyPending THEN <<"oc", "typed">> ELSE <<"oc">>
        /\ up = 1
        /\ name = IF LegacyPending THEN 2 ELSE 1
        /\ title = 1
        /\ gen = IF LegacyPending THEN 0 ELSE 1
        /\ syncedAt = 0
        /\ clock = 1
        /\ lock = "none"
        /\ retitled = 0
        /\ keptLocal = FALSE
        /\ seq = IF LegacyPending THEN 2 ELSE 1
        /\ upVer = 1
        /\ nameVer = IF LegacyPending THEN 2 ELSE 1
        /\ typedT = {}
        /\ markSaved = FALSE
        /\ pushFailures = PushFailures
        (* Process sync *)
        /\ started = 0
        /\ listed = 0
        /\ listedVer = 0
        /\ txNow = 0
        /\ passes = 0
        /\ finalPass = FALSE
        /\ finalDone = FALSE
        /\ pushName = 0
        (* Process retitle *)
        /\ judged = [self \in Retitlers |-> 0]
        /\ fin = [self \in Retitlers |-> 0]
        /\ stamp = [self \in Retitlers |-> 0]
        /\ finVer = [self \in Retitlers |-> 0]
        (* Process user *)
        /\ typed = [self \in Users |-> 0]
        /\ ustamp = [self \in Users |-> 0]
        /\ typedVer = [self \in Users |-> 0]
        /\ pc = [self \in ProcSet |-> CASE self = "sync" -> "Loop"
                                        [] self \in Retitlers -> "Read"
                                        [] self \in Users -> "UMark"
                                        [] self \in Renamers -> "Rename"]

Loop == /\ pc["sync"] = "Loop"
        /\ IF ~(WritersDone /\ finalDone)
              THEN /\ passes < MaxPasses \/ WritersDone
                   /\ finalPass' = WritersDone
                   /\ passes' = passes + 1
                   /\ pc' = [pc EXCEPT !["sync"] = "PushRead"]
              ELSE /\ pc' = [pc EXCEPT !["sync"] = "Done"]
                   /\ UNCHANGED << passes, finalPass >>
        /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, lock, 
                        retitled, keptLocal, seq, upVer, nameVer, typedT, 
                        markSaved, pushFailures, started, listed, listedVer, 
                        txNow, finalDone, pushName, judged, fin, stamp, finVer, 
                        typed, ustamp, typedVer >>

PushRead == /\ pc["sync"] = "PushRead"
            /\ pushName' = IF Pending THEN name ELSE 0
            /\ pc' = [pc EXCEPT !["sync"] = "PushLock"]
            /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, lock, 
                            retitled, keptLocal, seq, upVer, nameVer, typedT, 
                            markSaved, pushFailures, started, listed, 
                            listedVer, txNow, passes, finalPass, finalDone, 
                            judged, fin, stamp, finVer, typed, ustamp, 
                            typedVer >>

PushLock == /\ pc["sync"] = "PushLock"
            /\ IF pushName # 0 /\ PushLocks
                  THEN /\ lock = "none"
                       /\ IF name = pushName /\ Pending
                             THEN /\ lock' = "sync"
                                  /\ UNCHANGED pushName
                             ELSE /\ pushName' = 0
                                  /\ lock' = lock
                  ELSE /\ TRUE
                       /\ UNCHANGED << lock, pushName >>
            /\ pc' = [pc EXCEPT !["sync"] = "PushPatch"]
            /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, 
                            retitled, keptLocal, seq, upVer, nameVer, typedT, 
                            markSaved, pushFailures, started, listed, 
                            listedVer, txNow, passes, finalPass, finalDone, 
                            judged, fin, stamp, finVer, typed, ustamp, 
                            typedVer >>

PushPatch == /\ pc["sync"] = "PushPatch"
             /\ IF pushName # 0
                   THEN /\ \/ /\ up' = pushName
                              /\ seq' = seq + 1
                              /\ upVer' = seq'
                              /\ UNCHANGED <<lock, pushFailures, pushName>>
                           \/ /\ pushFailures > 0
                              /\ pushFailures' = pushFailures - 1
                              /\ pushName' = 0
                              /\ IF PushLocks
                                    THEN /\ lock' = "none"
                                    ELSE /\ TRUE
                                         /\ lock' = lock
                              /\ UNCHANGED <<up, seq, upVer>>
                   ELSE /\ TRUE
                        /\ UNCHANGED << up, lock, seq, upVer, pushFailures, 
                                        pushName >>
             /\ pc' = [pc EXCEPT !["sync"] = "PushUpdate"]
             /\ UNCHANGED << kinds, name, title, gen, syncedAt, clock, 
                             retitled, keptLocal, nameVer, typedT, markSaved, 
                             started, listed, listedVer, txNow, passes, 
                             finalPass, finalDone, judged, fin, stamp, finVer, 
                             typed, ustamp, typedVer >>

PushUpdate == /\ pc["sync"] = "PushUpdate"
              /\ IF pushName # 0
                    THEN /\ IF ~PushLocks
                               THEN /\ lock = "none"
                               ELSE /\ TRUE
                         /\ \/ /\ title' = name
                               /\ UNCHANGED pushFailures
                            \/ /\ CommitFail /\ pushFailures > 0
                               /\ pushFailures' = pushFailures - 1
                               /\ title' = title
                         /\ IF PushLocks
                               THEN /\ lock' = "none"
                               ELSE /\ TRUE
                                    /\ lock' = lock
                    ELSE /\ TRUE
                         /\ UNCHANGED << title, lock, pushFailures >>
              /\ pc' = [pc EXCEPT !["sync"] = "Started"]
              /\ UNCHANGED << kinds, up, name, gen, syncedAt, clock, retitled, 
                              keptLocal, seq, upVer, nameVer, typedT, 
                              markSaved, started, listed, listedVer, txNow, 
                              passes, finalPass, finalDone, pushName, judged, 
                              fin, stamp, finVer, typed, ustamp, typedVer >>

Started == /\ pc["sync"] = "Started"
           /\ started' = clock
           /\ clock' = clock + 1
           /\ pc' = [pc EXCEPT !["sync"] = "List"]
           /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, lock, 
                           retitled, keptLocal, seq, upVer, nameVer, typedT, 
                           markSaved, pushFailures, listed, listedVer, txNow, 
                           passes, finalPass, finalDone, pushName, judged, fin, 
                           stamp, finVer, typed, ustamp, typedVer >>

List == /\ pc["sync"] = "List"
        /\ listed' = up
        /\ listedVer' = upVer
        /\ pc' = [pc EXCEPT !["sync"] = "TxBegin"]
        /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, lock, 
                        retitled, keptLocal, seq, upVer, nameVer, typedT, 
                        markSaved, pushFailures, started, txNow, passes, 
                        finalPass, finalDone, pushName, judged, fin, stamp, 
                        finVer, typed, ustamp, typedVer >>

TxBegin == /\ pc["sync"] = "TxBegin"
           /\ txNow' = clock
           /\ clock' = clock + 1
           /\ pc' = [pc EXCEPT !["sync"] = "Upsert"]
           /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, lock, 
                           retitled, keptLocal, seq, upVer, nameVer, typedT, 
                           markSaved, pushFailures, started, listed, listedVer, 
                           passes, finalPass, finalDone, pushName, judged, fin, 
                           stamp, finVer, typed, ustamp, typedVer >>

Upsert == /\ pc["sync"] = "Upsert"
          /\ lock = "none"
          /\ LET keep == (syncedAt > started) IN
               LET keepName == (keep \/ (KeepPending /\ Pending)) IN
                 /\ IF keep /\ listed # name
                       THEN /\ keptLocal' = TRUE
                       ELSE /\ TRUE
                            /\ UNCHANGED keptLocal
                 /\ IF TypedMarks /\ ~keepName /\ listed \in typedT /\ listed # title
                       THEN /\ markSaved' = TRUE
                       ELSE /\ TRUE
                            /\ UNCHANGED markSaved
                 /\ gen' = (IF keepName \/ (~SyncAdoptsAlways /\ listed = title) \/ (TypedMarks /\ listed \in typedT) THEN gen ELSE listed)
                 /\ typedT' = IF keep THEN typedT ELSE typedT \ {listed}
                 /\ title' = IF keep THEN title ELSE listed
                 /\ name' = IF keepName THEN name ELSE listed
                 /\ nameVer' = IF keepName THEN nameVer ELSE listedVer
                 /\ syncedAt' = txNow
          /\ finalDone' = (finalPass /\ name' = title')
          /\ pc' = [pc EXCEPT !["sync"] = "Loop"]
          /\ UNCHANGED << kinds, up, clock, lock, retitled, seq, upVer, 
                          pushFailures, started, listed, listedVer, txNow, 
                          passes, finalPass, pushName, judged, fin, stamp, 
                          finVer, typed, ustamp, typedVer >>

sync == Loop \/ PushRead \/ PushLock \/ PushPatch \/ PushUpdate \/ Started
           \/ List \/ TxBegin \/ Upsert

Read(self) == /\ pc[self] = "Read"
              /\ IF name = gen
                    THEN /\ judged' = [judged EXCEPT ![self] = name]
                         /\ pc' = [pc EXCEPT ![self] = "Judge"]
                    ELSE /\ pc' = [pc EXCEPT ![self] = "Done"]
                         /\ UNCHANGED judged
              /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, 
                              lock, retitled, keptLocal, seq, upVer, nameVer, 
                              typedT, markSaved, pushFailures, started, listed, 
                              listedVer, txNow, passes, finalPass, finalDone, 
                              pushName, fin, stamp, finVer, typed, ustamp, 
                              typedVer >>

Judge(self) == /\ pc[self] = "Judge"
               /\ \/ /\ pc' = [pc EXCEPT ![self] = "Done"]
                  \/ /\ TRUE
                     /\ pc' = [pc EXCEPT ![self] = "Guard"]
               /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, 
                               lock, retitled, keptLocal, seq, upVer, nameVer, 
                               typedT, markSaved, pushFailures, started, 
                               listed, listedVer, txNow, passes, finalPass, 
                               finalDone, pushName, judged, fin, stamp, finVer, 
                               typed, ustamp, typedVer >>

Guard(self) == /\ pc[self] = "Guard"
               /\ lock = "none"
               /\ IF (GuardOff \/ name = gen) /\ (~JudgedGuard \/ name = judged[self])
                     THEN /\ lock' = self
                          /\ fin' = [fin EXCEPT ![self] = Len(kinds) + 1]
                          /\ kinds' = Append(kinds, "gen")
                          /\ IF StampFirst
                                THEN /\ stamp' = [stamp EXCEPT ![self] = clock]
                                     /\ clock' = clock + 1
                                ELSE /\ TRUE
                                     /\ UNCHANGED << clock, stamp >>
                          /\ pc' = [pc EXCEPT ![self] = "Patch"]
                     ELSE /\ pc' = [pc EXCEPT ![self] = "Done"]
                          /\ UNCHANGED << kinds, clock, lock, fin, stamp >>
               /\ UNCHANGED << up, name, title, gen, syncedAt, retitled, 
                               keptLocal, seq, upVer, nameVer, typedT, 
                               markSaved, pushFailures, started, listed, 
                               listedVer, txNow, passes, finalPass, finalDone, 
                               pushName, judged, finVer, typed, ustamp, 
                               typedVer >>

Patch(self) == /\ pc[self] = "Patch"
               /\ \/ /\ up' = fin[self]
                     /\ seq' = seq + 1
                     /\ upVer' = seq'
                     /\ finVer' = [finVer EXCEPT ![self] = seq']
                     /\ pc' = [pc EXCEPT ![self] = "Stamp"]
                     /\ lock' = lock
                  \/ /\ lock' = "none"
                     /\ pc' = [pc EXCEPT ![self] = "Done"]
                     /\ UNCHANGED <<up, seq, upVer, finVer>>
               /\ UNCHANGED << kinds, name, title, gen, syncedAt, clock, 
                               retitled, keptLocal, nameVer, typedT, markSaved, 
                               pushFailures, started, listed, listedVer, txNow, 
                               passes, finalPass, finalDone, pushName, judged, 
                               fin, stamp, typed, ustamp, typedVer >>

Stamp(self) == /\ pc[self] = "Stamp"
               /\ IF ~StampFirst
                     THEN /\ stamp' = [stamp EXCEPT ![self] = clock]
                          /\ clock' = clock + 1
                     ELSE /\ TRUE
                          /\ UNCHANGED << clock, stamp >>
               /\ pc' = [pc EXCEPT ![self] = "Commit"]
               /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, lock, 
                               retitled, keptLocal, seq, upVer, nameVer, 
                               typedT, markSaved, pushFailures, started, 
                               listed, listedVer, txNow, passes, finalPass, 
                               finalDone, pushName, judged, fin, finVer, typed, 
                               ustamp, typedVer >>

Commit(self) == /\ pc[self] = "Commit"
                /\ \/ /\ name' = fin[self]
                      /\ nameVer' = finVer[self]
                      /\ gen' = fin[self]
                      /\ title' = fin[self]
                      /\ syncedAt' = stamp[self]
                      /\ retitled' = retitled + 1
                      /\ lock' = "none"
                   \/ /\ CommitFail
                      /\ lock' = "none"
                      /\ UNCHANGED <<name, title, gen, syncedAt, retitled, nameVer>>
                /\ pc' = [pc EXCEPT ![self] = "Done"]
                /\ UNCHANGED << kinds, up, clock, keptLocal, seq, upVer, 
                                typedT, markSaved, pushFailures, started, 
                                listed, listedVer, txNow, passes, finalPass, 
                                finalDone, pushName, judged, fin, stamp, 
                                finVer, typed, ustamp, typedVer >>

retitle(self) == Read(self) \/ Judge(self) \/ Guard(self) \/ Patch(self)
                    \/ Stamp(self) \/ Commit(self)

UMark(self) == /\ pc[self] = "UMark"
               /\ typed' = [typed EXCEPT ![self] = Len(kinds) + 1]
               /\ kinds' = Append(kinds, "typed")
               /\ IF TypedMarks
                     THEN /\ typedT' = (typedT \cup {typed'[self]})
                     ELSE /\ TRUE
                          /\ UNCHANGED typedT
               /\ pc' = [pc EXCEPT ![self] = "ULock"]
               /\ UNCHANGED << up, name, title, gen, syncedAt, clock, lock, 
                               retitled, keptLocal, seq, upVer, nameVer, 
                               markSaved, pushFailures, started, listed, 
                               listedVer, txNow, passes, finalPass, finalDone, 
                               pushName, judged, fin, stamp, finVer, ustamp, 
                               typedVer >>

ULock(self) == /\ pc[self] = "ULock"
               /\ lock = "none"
               /\ lock' = self
               /\ pc' = [pc EXCEPT ![self] = "UPatch"]
               /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, clock, 
                               retitled, keptLocal, seq, upVer, nameVer, 
                               typedT, markSaved, pushFailures, started, 
                               listed, listedVer, txNow, passes, finalPass, 
                               finalDone, pushName, judged, fin, stamp, finVer, 
                               typed, ustamp, typedVer >>

UPatch(self) == /\ pc[self] = "UPatch"
                /\ \/ /\ up' = typed[self]
                      /\ seq' = seq + 1
                      /\ upVer' = seq'
                      /\ typedVer' = [typedVer EXCEPT ![self] = seq']
                      /\ pc' = [pc EXCEPT ![self] = "UStamp"]
                      /\ lock' = lock
                   \/ /\ lock' = "none"
                      /\ pc' = [pc EXCEPT ![self] = "Done"]
                      /\ UNCHANGED <<up, seq, upVer, typedVer>>
                /\ UNCHANGED << kinds, name, title, gen, syncedAt, clock, 
                                retitled, keptLocal, nameVer, typedT, 
                                markSaved, pushFailures, started, listed, 
                                listedVer, txNow, passes, finalPass, finalDone, 
                                pushName, judged, fin, stamp, finVer, typed, 
                                ustamp >>

UStamp(self) == /\ pc[self] = "UStamp"
                /\ ustamp' = [ustamp EXCEPT ![self] = clock]
                /\ clock' = clock + 1
                /\ pc' = [pc EXCEPT ![self] = "UCommit"]
                /\ UNCHANGED << kinds, up, name, title, gen, syncedAt, lock, 
                                retitled, keptLocal, seq, upVer, nameVer, 
                                typedT, markSaved, pushFailures, started, 
                                listed, listedVer, txNow, passes, finalPass, 
                                finalDone, pushName, judged, fin, stamp, 
                                finVer, typed, typedVer >>

UCommit(self) == /\ pc[self] = "UCommit"
                 /\ \/ /\ name' = typed[self]
                       /\ nameVer' = typedVer[self]
                       /\ title' = typed[self]
                       /\ typedT' = typedT \ {typed[self]}
                       /\ syncedAt' = ustamp[self]
                       /\ lock' = "none"
                    \/ /\ CommitFail
                       /\ lock' = "none"
                       /\ UNCHANGED <<name, title, syncedAt, nameVer, typedT>>
                 /\ pc' = [pc EXCEPT ![self] = "Done"]
                 /\ UNCHANGED << kinds, up, gen, clock, retitled, keptLocal, 
                                 seq, upVer, markSaved, pushFailures, started, 
                                 listed, listedVer, txNow, passes, finalPass, 
                                 finalDone, pushName, judged, fin, stamp, 
                                 finVer, typed, ustamp, typedVer >>

user(self) == UMark(self) \/ ULock(self) \/ UPatch(self) \/ UStamp(self)
                 \/ UCommit(self)

Rename(self) == /\ pc[self] = "Rename"
                /\ up' = Len(kinds) + 1
                /\ kinds' = Append(kinds, "oc")
                /\ seq' = seq + 1
                /\ upVer' = seq'
                /\ pc' = [pc EXCEPT ![self] = "Done"]
                /\ UNCHANGED << name, title, gen, syncedAt, clock, lock, 
                                retitled, keptLocal, nameVer, typedT, 
                                markSaved, pushFailures, started, listed, 
                                listedVer, txNow, passes, finalPass, finalDone, 
                                pushName, judged, fin, stamp, finVer, typed, 
                                ustamp, typedVer >>

renamer(self) == Rename(self)

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == sync
           \/ (\E self \in Retitlers: retitle(self))
           \/ (\E self \in Users: user(self))
           \/ (\E self \in Renamers: renamer(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(sync)
        /\ \A self \in Retitlers : WF_vars(retitle(self))
        /\ \A self \in Users : WF_vars(user(self))
        /\ \A self \in Renamers : WF_vars(renamer(self))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION

(* Safety: a typed name never counts as generated, so the title check never
   takes it. *)
TypedNotGenerated == kinds[name] = "typed" => gen # name

(* Safety: the check replaces only the name it judged. The lock is held from
   the guard to the commit, so the name cannot change in between. *)
ReplacesOnlyJudged ==
    \A r \in Retitlers : pc[r] \in {"Patch", "Stamp", "Commit"} => name = judged[r]

(* At the end, RocketClaw and OpenCode agree. *)
ConvergedAtEnd == pc["sync"] = "Done" => (name = up /\ title = up)

(* Safety (action): the local name never goes back to a title OpenCode held
   before the one it has, counting by when each title was written to OpenCode. *)
NameNeverOlder == [][nameVer' >= nameVer]_nameVer

(* Safety (action): a typed name is never replaced by a name typed before it. *)
LatestTypedWins == [][kinds[name] = "typed" /\ kinds'[name'] = "typed" => name' >= name]_<<name, kinds>>

(* Safety: a check never takes a typed name. From its guard to its commit it
   holds the lock, so the name it replaces is the one it holds. *)
ChecksNeverTakeTyped ==
    \A r \in Retitlers : pc[r] \in {"Patch", "Stamp", "Commit"} => kinds[name] # "typed"

(* Liveness: the sync loop finishes its last pass. *)
Terminates == <>(pc["sync"] = "Done")

(* Witnesses: each must be violated, showing the checks reach the case. *)
NeverRetitled == retitled = 0
NeverKeptLocal == ~keptLocal
NeverRacedAgainstCheck == ~(pc["sync"] = "Upsert" /\ lock \in Retitlers /\ listed # up)
NeverRefusedPush == pushFailures = PushFailures
NeverMarkSaved == ~markSaved
=============================================================================
