------------------------------ MODULE TabAttention ------------------------------
(* One person's attention state for one tab, as designed in
   docs/specs/2026-11-09-tab-attention-design.md. The server holds the mark,
   the park and the timer; each of the person's browser windows polls and
   decides on its own whether to raise a desktop pop-up. The tab may be a
   session or any other page; only a session has an agent that asks, so a
   page's behaviours are a subset of the ones checked here.

   The model is the PlusCal algorithm below; `make` translates it into the
   TLA+ between the TRANSLATION markers, which is generated, and checks each
   shape. Processes are what fairness attaches to: the person and the world act
   when they like, while the clock, the server's timer jobs, its loops that
   end parks at idle and put back the park a question ended, and each window's
   polls and reloads keep going, each fairly on its own.

   The CONSTANTS switch between implementation shapes, so the same
   properties can be checked against each of them:
   - ServerOwnsPark: the server writes no unread mark while parked, and
     stores apart that something happened.
   - ClientFiltersPark: a window skips the pop-up for a session it sees parked.
   - SameFeed: a window learns the park state in the same listing as the mark
     (once, mutes came from the Tabs call, marks from ListSessions).
   - JobChecksTimer: a firing timer job acts only while it is still the
     session's timer.
   - QuietMarks: the server decides which marks pop up. A mark written while
     parked, and a mark that parking settles, is quiet, like one the person
     set themselves.
   - QuietKeepsMark: an event that only turns the session bold leaves a mark
     that is already there alone.
   - SharedQuiet: a person who sees the tab only in a Shared group is reached
     without a pop-up. FALSE makes a Shared tab an ordinary tab of everyone
     who can see it, reached like their own.
   - TabPresence: the person can close the tab, up to MaxTabMoves times, and
     open it again; a due timer brings a closed tab back. A window learns
     which tabs the person has from its own reload of the tabs, apart from the
     marks. While the tab is closed an event still reaches the person as the
     sender of a turn, as a server must when the tab is on a Top Shelf of
     another server it knows nothing of.
   - DeferStale: the session listing names the newest tab-state revision, and
     a window that holds an older one decides no pop-up until it reloads.
   - ReopenQuiets: opening a closed tab again makes the mark it kept quiet.
   - QuestionOnClosed: what an agent's question does to a closed parked tab: "unpark"
     ends its park as on an open tab, "restore" brings it back unparked and pops up,
     "keep" leaves its park and timer, so it comes back at its time. "due" ends any
     park, open or closed, by making it due now, so the person's own timer job ends it
     and brings a closed tab back.
   - WakeReaches: a turn that no person's prompt started, such as the one a background
     job's result wakes the session with, reaches the session's readers when it ends, as
     one a person prompted does. FALSE is the OpenCode sync, which turns an idle marker
     into readers only through the stamped prompts before it.
   - EndsAtIdle: a park until it is done ends once its session is idle after a turn ended, or at a
     failed turn. FALSE is the earlier rule: any turn end or news on the session ends it. *)
EXTENDS Naturals

CONSTANTS Windows, MaxTime, MaxEvents, MaxTabMoves,
          ServerOwnsPark, ClientFiltersPark, SameFeed, JobChecksTimer, QuietMarks, QuietKeepsMark,
          SharedQuiet, TabPresence, DeferStale, ReopenQuiets, QuestionOnClosed, WakeReaches, EndsAtIdle

None   == [kind |-> "none", at |-> 0]
\* A park until the session is idle after its work, which no clock ends: it has no time and no job.
Done   == [kind |-> "done", at |-> 0]
\* No park to put back.
Off    == [kind |-> "off", at |-> 0]
Timers == {None, Done} \cup [kind : {"park"}, at : 0..MaxTime]

(* --algorithm TabAttention
variables
    now = 0,             \* the clock
    seq = 0,             \* last unread stamp handed out; stamps only grow, like now()
    unreadAt = 0,        \* 0, or the stamp of the current mark: bold unless parked
    quiet = FALSE,       \* the current mark raises no pop-up
    hold = FALSE,        \* a mark set on the session in view, kept until they leave
    parked = FALSE,
    happened = FALSE,    \* ServerOwnsPark only: something needed the person while parked
    timer = None,        \* the park's timer: None, a park time, or until it is done
    jobs = {},           \* timer jobs the server has scheduled and not yet run
    asking = FALSE,      \* an agent waits on a question
    busy = FALSE,        \* the session has a turn running, a message queued or a background job to wake it
    worked = FALSE,      \* a turn ended since the park until it is done began, or since the one a question ended
    resume = Off,        \* the park a question ended, to put back once no question waits; Off when none
    viewing = FALSE,     \* the session is active in a focused window
    hasTab = TRUE,       \* the person has the tab: in their tabs, Shared, or on a Top Shelf
    tabRev = 0,          \* the person's tab-state revision, raised by every close and reopen
    lastStamp = [w \in Windows |-> 0],    \* per window: the mark it last saw
    parkView = [w \in Windows |-> FALSE], \* per window: the park state it last saw
    tabView = [w \in Windows |-> TRUE],   \* per window: whether it holds the tab, as of its last reload of the tabs
    knownRev = [w \in Windows |-> 0],     \* per window: the tab-state revision of that reload
    owed = FALSE,        \* ghost: a mark that should pop up has not yet popped
    settled = 0,         \* ghost: the newest mark that must never pop up
    poppedParked = FALSE,  \* ghost: a pop-up was raised while the session was parked
    poppedSettled = FALSE, \* ghost: a pop-up was raised for a settled mark
    poppedNoTab = FALSE,   \* ghost: a pop-up was raised while the person had no tab
    staleFire = FALSE,     \* ghost: a timer job acted after its timer was replaced or cancelled
    earlyEnd = FALSE;      \* ghost: a park until it is done ended while the session was busy, not by a failure or a question

define
    Bold == unreadAt # 0 /\ ~parked
    ParkChoices   == {None, Done} \cup {[kind |-> "park", at |-> a] : a \in (now + 1)..MaxTime}
end define;

\* A new mark on a session that is not parked, for a person who has the tab or not. A quiet
\* one never owes a pop-up, and one written while the person has no tab must never pop up,
\* also once it is back.
macro Write(isQuiet, tab) begin
    seq := seq + 1 || unreadAt := seq + 1 || quiet := isQuiet
        || owed := owed \/ (~isQuiet /\ tab) || settled := IF tab THEN settled ELSE seq + 1;
end macro;

\* A new mark on a parked session. It must never pop up.
macro ParkedWrite() begin
    seq := seq + 1 || unreadAt := seq + 1 || quiet := QuietMarks || settled := seq + 1;
end macro;

macro Clear() begin
    unreadAt := 0 || quiet := FALSE || owed := FALSE;
end macro;

\* The world and the person, who act when they like.
process Person = <<"person">>
begin
PersonActs:
    while TRUE do
        either
            \* Work starts: a person sends or queues a message, or a background job will wake the agent.
            await ~busy;
            busy := TRUE;
        or
            \* A turn ends, leaving more work or not, failed or not, and takes any question with it. Whoever
            \* it reaches, it records that work happened for a park until it is done, also one a question
            \* ended; a failure ends a park until it is done at once, and drops the park a question ended. One
            \* a person's prompt started reaches this person with a mark: they sent it, they have the tab, or
            \* they parked it until it is done. One a background job's result woke the session with has no
            \* sender, and reaches them only when WakeReaches. Nobody is marked for a session they are viewing.
            await busy /\ seq < MaxEvents;
            with woke \in BOOLEAN, more \in BOOLEAN, failed \in BOOLEAN do
                busy := more || asking := FALSE;
                worked := worked \/ (parked /\ timer = Done) \/ resume = Done;
                if parked /\ timer = Done /\ (failed \/ ~EndsAtIdle) then
                    timer := [kind |-> "park", at |-> now] || jobs := jobs \cup {[kind |-> "park", at |-> now]}
                        || earlyEnd := earlyEnd \/ (more /\ ~failed);
                end if;
                if failed then resume := Off end if;
                if ~woke \/ WakeReaches then
                    if ~viewing /\ parked then
                        happened := TRUE;
                        if ~ServerOwnsPark then ParkedWrite() end if;
                    elsif ~viewing then
                        Write(FALSE, hasTab);
                    end if;
                end if;
            end with;
        or
            \* News that is not the agent, such as people talking while the agent is paused, reaches the
            \* person who has the tab or parked it until it is done. It leaves every park alone.
            await seq < MaxEvents /\ ~viewing /\ (hasTab \/ (parked /\ timer = Done));
            if parked /\ timer = Done /\ ~EndsAtIdle then
                timer := [kind |-> "park", at |-> now] || jobs := jobs \cup {[kind |-> "park", at |-> now]}
                    || earlyEnd := earlyEnd \/ busy;
            end if;
            if parked then
                happened := TRUE;
                if ~ServerOwnsPark then ParkedWrite() end if;
            else
                Write(FALSE, hasTab);
            end if;
        or
            \* Something new that only turns the tab bold for this person, with no pop-up: they see
            \* the tab only in a Shared group. It never unparks, also when it is an agent's question.
            await SharedQuiet /\ hasTab /\ seq < MaxEvents /\ ~viewing;
            if ~parked then
                if unreadAt = 0 then
                    Write(TRUE, TRUE);
                elsif ~QuietKeepsMark then
                    seq := seq + 1 || unreadAt := seq + 1 || quiet := TRUE;
                end if;
            else
                happened := TRUE;
                if ~ServerOwnsPark /\ unreadAt = 0 then
                    seq := seq + 1 || unreadAt := seq + 1 || quiet := TRUE || settled := seq + 1;
                end if;
            end if;
        or
            \* A running turn's agent asks. A question breaks a park, and the server remembers the park it
            \* ended; on a closed tab as QuestionOnClosed says.
            await busy /\ seq < MaxEvents /\ ~asking;
            asking := TRUE;
            if parked /\ QuestionOnClosed = "due" then
                timer := [kind |-> "park", at |-> now] || jobs := jobs \cup {[kind |-> "park", at |-> now]}
                    || resume := IF resume = Off THEN timer ELSE resume;
                if ~viewing then ParkedWrite() end if;
            elsif ~hasTab /\ parked /\ QuestionOnClosed = "keep" then
                ParkedWrite();
            else
                if ~hasTab /\ parked /\ QuestionOnClosed = "restore" then
                    hasTab := TRUE || tabRev := tabRev + 1;
                    Write(FALSE, TRUE);
                elsif ~viewing then
                    Write(FALSE, hasTab);
                end if;
                parked := FALSE || happened := FALSE || timer := None;
            end if;
        or
            \* Anyone answers, also someone who shares the session.
            await asking;
            asking := FALSE;
        or
            \* Marking the tab, unread or read, is the person's own choice: the park a question ended stays ended.
            await hasTab /\ seq < MaxEvents /\ unreadAt = 0 /\ ~parked;
            Write(TRUE, TRUE);
            hold := viewing || resume := Off;
        or
            await hasTab /\ unreadAt # 0;
            Clear();
            hold := FALSE || happened := FALSE || resume := Off;
        or
            \* Viewing clears the mark, and what happened while parked: the person saw it.
            await hasTab /\ ~viewing;
            viewing := TRUE || happened := FALSE;
            Clear();
        or
            await viewing;
            viewing := FALSE || hold := FALSE;
        or
            \* Parking settles the mark it finds: that mark never pops up.
            await hasTab /\ ~parked;
            with t \in ParkChoices do
                parked := TRUE || happened := FALSE || timer := t || owed := FALSE || worked := FALSE || resume := Off
                    || jobs := IF t \in {None, Done} THEN jobs ELSE jobs \cup {t}
                    || settled := IF unreadAt # 0 THEN unreadAt ELSE settled
                    || quiet := IF QuietMarks /\ unreadAt # 0 THEN TRUE ELSE quiet;
            end with;
        or
            \* Unparking leaves what happened bold, without a pop-up.
            await hasTab /\ parked;
            if ServerOwnsPark /\ happened /\ unreadAt = 0 /\ ~viewing then Write(TRUE, TRUE) end if;
            parked := FALSE || happened := FALSE || timer := None || resume := Off;
        or
            \* Closing keeps the mark, a timed park and a park until it is done; it ends a park until
            \* unparked, also one a question ended, and drops a pop-up still owed: with no tab, nothing
            \* pops up.
            await TabPresence /\ hasTab /\ tabRev < 2 * MaxTabMoves;
            if parked /\ timer = None then parked := FALSE || happened := FALSE end if;
            if resume = None then resume := Off end if;
            hasTab := FALSE || tabRev := tabRev + 1 || viewing := FALSE || hold := FALSE || owed := FALSE;
        or
            \* Opening the tab again shows the mark it kept, bold; when the reopen settles it, quietly.
            await TabPresence /\ ~hasTab;
            hasTab := TRUE || tabRev := tabRev + 1 || quiet := IF ReopenQuiets /\ unreadAt # 0 THEN TRUE ELSE quiet;
        end either;
    end while;
end process;

fair process Clock = <<"clock">>
begin
Ticks:
    while TRUE do
        await now < MaxTime;
        now := now + 1;
    end while;
end process;

\* The server runs a due timer job: the tab wakes, turns bold and pops up, and a closed tab comes
\* back.
fair process TimerJobs = <<"timers">>
begin
JobRuns:
    while TRUE do
        with j \in jobs do
            await j.at <= now;
            jobs := jobs \ {j};
            if ~JobChecksTimer \/ j = timer then
                staleFire := staleFire \/ j # timer;
                if ~viewing then Write(FALSE, TRUE) end if;
                timer := None || parked := FALSE || happened := FALSE || hasTab := TRUE
                    || tabRev := IF hasTab THEN tabRev ELSE tabRev + 1;
            end if;
        end with;
    end while;
end process;

\* The server ends a park until it is done once its session is idle after a turn ended, at the time
\* of its mark, so the reply's own pop-up announces it.
fair process IdleEnds = <<"idle">>
begin
IdleEnd:
    while TRUE do
        await EndsAtIdle /\ parked /\ timer = Done /\ worked /\ ~busy;
        timer := [kind |-> "park", at |-> now] || jobs := jobs \cup {[kind |-> "park", at |-> now]};
    end while;
end process;

\* Once no question waits, the server puts back the park a question ended, but not a timed one
\* whose time passed. Parking again settles the mark it finds; a park until it is done whose session
\* already finished its work then ends at the next idle check.
fair process Restores = <<"restore">>
begin
Restore:
    while TRUE do
        await resume # Off /\ ~asking /\ ~parked;
        if resume.kind = "park" /\ resume.at <= now then
            resume := Off;
        else
            parked := TRUE || happened := FALSE || timer := resume || owed := FALSE || resume := Off
                || jobs := IF resume \in {None, Done} THEN jobs ELSE jobs \cup {resume}
                || settled := IF unreadAt # 0 THEN unreadAt ELSE settled
                || quiet := IF QuietMarks /\ unreadAt # 0 THEN TRUE ELSE quiet;
        end if;
    end while;
end process;

\* A window lists sessions and pops up for a mark it has not seen, unless the mark is
\* quiet, (when it filters) it sees the session parked, or it holds no tab for it. Holding
\* an older tab state than the listing names, it waits for its reload (when it defers).
fair process Rows \in {<<"rows", w>> : w \in Windows}
begin
RowsPoll:
    while TRUE do
        with w = self[2], stale = DeferStale /\ knownRev[self[2]] < tabRev,
             seenParked = IF SameFeed THEN parked ELSE parkView[self[2]],
             pop = ~stale /\ unreadAt # 0 /\ unreadAt # lastStamp[self[2]] /\ ~quiet
                   /\ ~(ClientFiltersPark /\ seenParked) /\ tabView[self[2]] do
            if ~stale then lastStamp[w] := unreadAt end if;
            if SameFeed then parkView[w] := parked end if;
            owed := owed /\ ~pop || poppedParked := poppedParked \/ (pop /\ parked)
                || poppedSettled := poppedSettled \/ (pop /\ unreadAt <= settled)
                || poppedNoTab := poppedNoTab \/ (pop /\ ~hasTab);
        end with;
    end while;
end process;

\* With separate feeds, the park state arrives on its own poll.
fair process Tabs \in {<<"tabs", w>> : w \in Windows}
begin
TabsPoll:
    while TRUE do
        await ~SameFeed;
        parkView[self[2]] := parked;
    end while;
end process;

\* A window reloads the person's tabs.
fair process Reload \in {<<"reload", w>> : w \in Windows}
begin
TabsReload:
    while TRUE do
        await TabPresence /\ <<tabView[self[2]], knownRev[self[2]]>> # <<hasTab, tabRev>>;
        tabView[self[2]] := hasTab;
        knownRev[self[2]] := tabRev;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES now, seq, unreadAt, quiet, hold, parked, happened, timer, jobs, 
          asking, busy, worked, resume, viewing, hasTab, tabRev, lastStamp, 
          parkView, tabView, knownRev, owed, settled, poppedParked, 
          poppedSettled, poppedNoTab, staleFire, earlyEnd

(* define statement *)
Bold == unreadAt # 0 /\ ~parked
ParkChoices   == {None, Done} \cup {[kind |-> "park", at |-> a] : a \in (now + 1)..MaxTime}


vars == << now, seq, unreadAt, quiet, hold, parked, happened, timer, jobs, 
           asking, busy, worked, resume, viewing, hasTab, tabRev, lastStamp, 
           parkView, tabView, knownRev, owed, settled, poppedParked, 
           poppedSettled, poppedNoTab, staleFire, earlyEnd >>

ProcSet == {<<"person">>} \cup {<<"clock">>} \cup {<<"timers">>} \cup {<<"idle">>} \cup {<<"restore">>} \cup ({<<"rows", w>> : w \in Windows}) \cup ({<<"tabs", w>> : w \in Windows}) \cup ({<<"reload", w>> : w \in Windows})

Init == (* Global variables *)
        /\ now = 0
        /\ seq = 0
        /\ unreadAt = 0
        /\ quiet = FALSE
        /\ hold = FALSE
        /\ parked = FALSE
        /\ happened = FALSE
        /\ timer = None
        /\ jobs = {}
        /\ asking = FALSE
        /\ busy = FALSE
        /\ worked = FALSE
        /\ resume = Off
        /\ viewing = FALSE
        /\ hasTab = TRUE
        /\ tabRev = 0
        /\ lastStamp = [w \in Windows |-> 0]
        /\ parkView = [w \in Windows |-> FALSE]
        /\ tabView = [w \in Windows |-> TRUE]
        /\ knownRev = [w \in Windows |-> 0]
        /\ owed = FALSE
        /\ settled = 0
        /\ poppedParked = FALSE
        /\ poppedSettled = FALSE
        /\ poppedNoTab = FALSE
        /\ staleFire = FALSE
        /\ earlyEnd = FALSE

Person == /\ \/ /\ ~busy
                /\ busy' = TRUE
                /\ UNCHANGED <<seq, unreadAt, quiet, hold, parked, happened, timer, jobs, asking, worked, resume, viewing, hasTab, tabRev, owed, settled, earlyEnd>>
             \/ /\ busy /\ seq < MaxEvents
                /\ \E woke \in BOOLEAN:
                     \E more \in BOOLEAN:
                       \E failed \in BOOLEAN:
                         /\ /\ asking' = FALSE
                            /\ busy' = more
                         /\ worked' = (worked \/ (parked /\ timer = Done) \/ resume = Done)
                         /\ IF parked /\ timer = Done /\ (failed \/ ~EndsAtIdle)
                               THEN /\ /\ earlyEnd' = (earlyEnd \/ (more /\ ~failed))
                                       /\ jobs' = (jobs \cup {[kind |-> "park", at |-> now]})
                                       /\ timer' = [kind |-> "park", at |-> now]
                               ELSE /\ TRUE
                                    /\ UNCHANGED << timer, jobs, earlyEnd >>
                         /\ IF failed
                               THEN /\ resume' = Off
                               ELSE /\ TRUE
                                    /\ UNCHANGED resume
                         /\ IF ~woke \/ WakeReaches
                               THEN /\ IF ~viewing /\ parked
                                          THEN /\ happened' = TRUE
                                               /\ IF ~ServerOwnsPark
                                                     THEN /\ /\ quiet' = QuietMarks
                                                             /\ seq' = seq + 1
                                                             /\ settled' = seq + 1
                                                             /\ unreadAt' = seq + 1
                                                     ELSE /\ TRUE
                                                          /\ UNCHANGED << seq, 
                                                                          unreadAt, 
                                                                          quiet, 
                                                                          settled >>
                                               /\ owed' = owed
                                          ELSE /\ IF ~viewing
                                                     THEN /\ /\ owed' = (owed \/ (~FALSE /\ hasTab))
                                                             /\ quiet' = FALSE
                                                             /\ seq' = seq + 1
                                                             /\ settled' = IF hasTab THEN settled ELSE seq + 1
                                                             /\ unreadAt' = seq + 1
                                                     ELSE /\ TRUE
                                                          /\ UNCHANGED << seq, 
                                                                          unreadAt, 
                                                                          quiet, 
                                                                          owed, 
                                                                          settled >>
                                               /\ UNCHANGED happened
                               ELSE /\ TRUE
                                    /\ UNCHANGED << seq, unreadAt, quiet, 
                                                    happened, owed, settled >>
                /\ UNCHANGED <<hold, parked, viewing, hasTab, tabRev>>
             \/ /\ seq < MaxEvents /\ ~viewing /\ (hasTab \/ (parked /\ timer = Done))
                /\ IF parked /\ timer = Done /\ ~EndsAtIdle
                      THEN /\ /\ earlyEnd' = (earlyEnd \/ busy)
                              /\ jobs' = (jobs \cup {[kind |-> "park", at |-> now]})
                              /\ timer' = [kind |-> "park", at |-> now]
                      ELSE /\ TRUE
                           /\ UNCHANGED << timer, jobs, earlyEnd >>
                /\ IF parked
                      THEN /\ happened' = TRUE
                           /\ IF ~ServerOwnsPark
                                 THEN /\ /\ quiet' = QuietMarks
                                         /\ seq' = seq + 1
                                         /\ settled' = seq + 1
                                         /\ unreadAt' = seq + 1
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << seq, unreadAt, quiet, 
                                                      settled >>
                           /\ owed' = owed
                      ELSE /\ /\ owed' = (owed \/ (~FALSE /\ hasTab))
                              /\ quiet' = FALSE
                              /\ seq' = seq + 1
                              /\ settled' = IF hasTab THEN settled ELSE seq + 1
                              /\ unreadAt' = seq + 1
                           /\ UNCHANGED happened
                /\ UNCHANGED <<hold, parked, asking, busy, worked, resume, viewing, hasTab, tabRev>>
             \/ /\ SharedQuiet /\ hasTab /\ seq < MaxEvents /\ ~viewing
                /\ IF ~parked
                      THEN /\ IF unreadAt = 0
                                 THEN /\ /\ owed' = (owed \/ (~TRUE /\ TRUE))
                                         /\ quiet' = TRUE
                                         /\ seq' = seq + 1
                                         /\ settled' = IF TRUE THEN settled ELSE seq + 1
                                         /\ unreadAt' = seq + 1
                                 ELSE /\ IF ~QuietKeepsMark
                                            THEN /\ /\ quiet' = TRUE
                                                    /\ seq' = seq + 1
                                                    /\ unreadAt' = seq + 1
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED << seq, unreadAt, 
                                                                 quiet >>
                                      /\ UNCHANGED << owed, settled >>
                           /\ UNCHANGED happened
                      ELSE /\ happened' = TRUE
                           /\ IF ~ServerOwnsPark /\ unreadAt = 0
                                 THEN /\ /\ quiet' = TRUE
                                         /\ seq' = seq + 1
                                         /\ settled' = seq + 1
                                         /\ unreadAt' = seq + 1
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << seq, unreadAt, quiet, 
                                                      settled >>
                           /\ owed' = owed
                /\ UNCHANGED <<hold, parked, timer, jobs, asking, busy, worked, resume, viewing, hasTab, tabRev, earlyEnd>>
             \/ /\ busy /\ seq < MaxEvents /\ ~asking
                /\ asking' = TRUE
                /\ IF parked /\ QuestionOnClosed = "due"
                      THEN /\ /\ jobs' = (jobs \cup {[kind |-> "park", at |-> now]})
                              /\ resume' = (IF resume = Off THEN timer ELSE resume)
                              /\ timer' = [kind |-> "park", at |-> now]
                           /\ IF ~viewing
                                 THEN /\ /\ quiet' = QuietMarks
                                         /\ seq' = seq + 1
                                         /\ settled' = seq + 1
                                         /\ unreadAt' = seq + 1
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << seq, unreadAt, quiet, 
                                                      settled >>
                           /\ UNCHANGED << parked, happened, hasTab, tabRev, 
                                           owed >>
                      ELSE /\ IF ~hasTab /\ parked /\ QuestionOnClosed = "keep"
                                 THEN /\ /\ quiet' = QuietMarks
                                         /\ seq' = seq + 1
                                         /\ settled' = seq + 1
                                         /\ unreadAt' = seq + 1
                                      /\ UNCHANGED << parked, happened, timer, 
                                                      hasTab, tabRev, owed >>
                                 ELSE /\ IF ~hasTab /\ parked /\ QuestionOnClosed = "restore"
                                            THEN /\ /\ hasTab' = TRUE
                                                    /\ tabRev' = tabRev + 1
                                                 /\ /\ owed' = (owed \/ (~FALSE /\ TRUE))
                                                    /\ quiet' = FALSE
                                                    /\ seq' = seq + 1
                                                    /\ settled' = IF TRUE THEN settled ELSE seq + 1
                                                    /\ unreadAt' = seq + 1
                                            ELSE /\ IF ~viewing
                                                       THEN /\ /\ owed' = (owed \/ (~FALSE /\ hasTab))
                                                               /\ quiet' = FALSE
                                                               /\ seq' = seq + 1
                                                               /\ settled' = IF hasTab THEN settled ELSE seq + 1
                                                               /\ unreadAt' = seq + 1
                                                       ELSE /\ TRUE
                                                            /\ UNCHANGED << seq, 
                                                                            unreadAt, 
                                                                            quiet, 
                                                                            owed, 
                                                                            settled >>
                                                 /\ UNCHANGED << hasTab, 
                                                                 tabRev >>
                                      /\ /\ happened' = FALSE
                                         /\ parked' = FALSE
                                         /\ timer' = None
                           /\ UNCHANGED << jobs, resume >>
                /\ UNCHANGED <<hold, busy, worked, viewing, earlyEnd>>
             \/ /\ asking
                /\ asking' = FALSE
                /\ UNCHANGED <<seq, unreadAt, quiet, hold, parked, happened, timer, jobs, busy, worked, resume, viewing, hasTab, tabRev, owed, settled, earlyEnd>>
             \/ /\ hasTab /\ seq < MaxEvents /\ unreadAt = 0 /\ ~parked
                /\ /\ owed' = (owed \/ (~TRUE /\ TRUE))
                   /\ quiet' = TRUE
                   /\ seq' = seq + 1
                   /\ settled' = IF TRUE THEN settled ELSE seq + 1
                   /\ unreadAt' = seq + 1
                /\ /\ hold' = viewing
                   /\ resume' = Off
                /\ UNCHANGED <<parked, happened, timer, jobs, asking, busy, worked, viewing, hasTab, tabRev, earlyEnd>>
             \/ /\ hasTab /\ unreadAt # 0
                /\ /\ owed' = FALSE
                   /\ quiet' = FALSE
                   /\ unreadAt' = 0
                /\ /\ happened' = FALSE
                   /\ hold' = FALSE
                   /\ resume' = Off
                /\ UNCHANGED <<seq, parked, timer, jobs, asking, busy, worked, viewing, hasTab, tabRev, settled, earlyEnd>>
             \/ /\ hasTab /\ ~viewing
                /\ /\ happened' = FALSE
                   /\ viewing' = TRUE
                /\ /\ owed' = FALSE
                   /\ quiet' = FALSE
                   /\ unreadAt' = 0
                /\ UNCHANGED <<seq, hold, parked, timer, jobs, asking, busy, worked, resume, hasTab, tabRev, settled, earlyEnd>>
             \/ /\ viewing
                /\ /\ hold' = FALSE
                   /\ viewing' = FALSE
                /\ UNCHANGED <<seq, unreadAt, quiet, parked, happened, timer, jobs, asking, busy, worked, resume, hasTab, tabRev, owed, settled, earlyEnd>>
             \/ /\ hasTab /\ ~parked
                /\ \E t \in ParkChoices:
                     /\ happened' = FALSE
                     /\ jobs' = (IF t \in {None, Done} THEN jobs ELSE jobs \cup {t})
                     /\ owed' = FALSE
                     /\ parked' = TRUE
                     /\ quiet' = (IF QuietMarks /\ unreadAt # 0 THEN TRUE ELSE quiet)
                     /\ resume' = Off
                     /\ settled' = (IF unreadAt # 0 THEN unreadAt ELSE settled)
                     /\ timer' = t
                     /\ worked' = FALSE
                /\ UNCHANGED <<seq, unreadAt, hold, asking, busy, viewing, hasTab, tabRev, earlyEnd>>
             \/ /\ hasTab /\ parked
                /\ IF ServerOwnsPark /\ happened /\ unreadAt = 0 /\ ~viewing
                      THEN /\ /\ owed' = (owed \/ (~TRUE /\ TRUE))
                              /\ quiet' = TRUE
                              /\ seq' = seq + 1
                              /\ settled' = IF TRUE THEN settled ELSE seq + 1
                              /\ unreadAt' = seq + 1
                      ELSE /\ TRUE
                           /\ UNCHANGED << seq, unreadAt, quiet, owed, settled >>
                /\ /\ happened' = FALSE
                   /\ parked' = FALSE
                   /\ resume' = Off
                   /\ timer' = None
                /\ UNCHANGED <<hold, jobs, asking, busy, worked, viewing, hasTab, tabRev, earlyEnd>>
             \/ /\ TabPresence /\ hasTab /\ tabRev < 2 * MaxTabMoves
                /\ IF parked /\ timer = None
                      THEN /\ /\ happened' = FALSE
                              /\ parked' = FALSE
                      ELSE /\ TRUE
                           /\ UNCHANGED << parked, happened >>
                /\ IF resume = None
                      THEN /\ resume' = Off
                      ELSE /\ TRUE
                           /\ UNCHANGED resume
                /\ /\ hasTab' = FALSE
                   /\ hold' = FALSE
                   /\ owed' = FALSE
                   /\ tabRev' = tabRev + 1
                   /\ viewing' = FALSE
                /\ UNCHANGED <<seq, unreadAt, quiet, timer, jobs, asking, busy, worked, settled, earlyEnd>>
             \/ /\ TabPresence /\ ~hasTab
                /\ /\ hasTab' = TRUE
                   /\ quiet' = (IF ReopenQuiets /\ unreadAt # 0 THEN TRUE ELSE quiet)
                   /\ tabRev' = tabRev + 1
                /\ UNCHANGED <<seq, unreadAt, hold, parked, happened, timer, jobs, asking, busy, worked, resume, viewing, owed, settled, earlyEnd>>
          /\ UNCHANGED << now, lastStamp, parkView, tabView, knownRev, 
                          poppedParked, poppedSettled, poppedNoTab, staleFire >>

Clock == /\ now < MaxTime
         /\ now' = now + 1
         /\ UNCHANGED << seq, unreadAt, quiet, hold, parked, happened, timer, 
                         jobs, asking, busy, worked, resume, viewing, hasTab, 
                         tabRev, lastStamp, parkView, tabView, knownRev, owed, 
                         settled, poppedParked, poppedSettled, poppedNoTab, 
                         staleFire, earlyEnd >>

TimerJobs == /\ \E j \in jobs:
                  /\ j.at <= now
                  /\ jobs' = jobs \ {j}
                  /\ IF ~JobChecksTimer \/ j = timer
                        THEN /\ staleFire' = (staleFire \/ j # timer)
                             /\ IF ~viewing
                                   THEN /\ /\ owed' = (owed \/ (~FALSE /\ TRUE))
                                           /\ quiet' = FALSE
                                           /\ seq' = seq + 1
                                           /\ settled' = IF TRUE THEN settled ELSE seq + 1
                                           /\ unreadAt' = seq + 1
                                   ELSE /\ TRUE
                                        /\ UNCHANGED << seq, unreadAt, quiet, 
                                                        owed, settled >>
                             /\ /\ happened' = FALSE
                                /\ hasTab' = TRUE
                                /\ parked' = FALSE
                                /\ tabRev' = IF hasTab THEN tabRev ELSE tabRev + 1
                                /\ timer' = None
                        ELSE /\ TRUE
                             /\ UNCHANGED << seq, unreadAt, quiet, parked, 
                                             happened, timer, hasTab, tabRev, 
                                             owed, settled, staleFire >>
             /\ UNCHANGED << now, hold, asking, busy, worked, resume, viewing, 
                             lastStamp, parkView, tabView, knownRev, 
                             poppedParked, poppedSettled, poppedNoTab, 
                             earlyEnd >>

IdleEnds == /\ EndsAtIdle /\ parked /\ timer = Done /\ worked /\ ~busy
            /\ /\ jobs' = (jobs \cup {[kind |-> "park", at |-> now]})
               /\ timer' = [kind |-> "park", at |-> now]
            /\ UNCHANGED << now, seq, unreadAt, quiet, hold, parked, happened, 
                            asking, busy, worked, resume, viewing, hasTab, 
                            tabRev, lastStamp, parkView, tabView, knownRev, 
                            owed, settled, poppedParked, poppedSettled, 
                            poppedNoTab, staleFire, earlyEnd >>

Restores == /\ resume # Off /\ ~asking /\ ~parked
            /\ IF resume.kind = "park" /\ resume.at <= now
                  THEN /\ resume' = Off
                       /\ UNCHANGED << quiet, parked, happened, timer, jobs, 
                                       owed, settled >>
                  ELSE /\ /\ happened' = FALSE
                          /\ jobs' = (IF resume \in {None, Done} THEN jobs ELSE jobs \cup {resume})
                          /\ owed' = FALSE
                          /\ parked' = TRUE
                          /\ quiet' = (IF QuietMarks /\ unreadAt # 0 THEN TRUE ELSE quiet)
                          /\ resume' = Off
                          /\ settled' = (IF unreadAt # 0 THEN unreadAt ELSE settled)
                          /\ timer' = resume
            /\ UNCHANGED << now, seq, unreadAt, hold, asking, busy, worked, 
                            viewing, hasTab, tabRev, lastStamp, parkView, 
                            tabView, knownRev, poppedParked, poppedSettled, 
                            poppedNoTab, staleFire, earlyEnd >>

Rows(self) == /\ LET w == self[2] IN
                   LET stale == DeferStale /\ knownRev[self[2]] < tabRev IN
                     LET seenParked == IF SameFeed THEN parked ELSE parkView[self[2]] IN
                       LET pop == ~stale /\ unreadAt # 0 /\ unreadAt # lastStamp[self[2]] /\ ~quiet
                                  /\ ~(ClientFiltersPark /\ seenParked) /\ tabView[self[2]] IN
                         /\ IF ~stale
                               THEN /\ lastStamp' = [lastStamp EXCEPT ![w] = unreadAt]
                               ELSE /\ TRUE
                                    /\ UNCHANGED lastStamp
                         /\ IF SameFeed
                               THEN /\ parkView' = [parkView EXCEPT ![w] = parked]
                               ELSE /\ TRUE
                                    /\ UNCHANGED parkView
                         /\ /\ owed' = (owed /\ ~pop)
                            /\ poppedNoTab' = (poppedNoTab \/ (pop /\ ~hasTab))
                            /\ poppedParked' = (poppedParked \/ (pop /\ parked))
                            /\ poppedSettled' = (poppedSettled \/ (pop /\ unreadAt <= settled))
              /\ UNCHANGED << now, seq, unreadAt, quiet, hold, parked, 
                              happened, timer, jobs, asking, busy, worked, 
                              resume, viewing, hasTab, tabRev, tabView, 
                              knownRev, settled, staleFire, earlyEnd >>

Tabs(self) == /\ ~SameFeed
              /\ parkView' = [parkView EXCEPT ![self[2]] = parked]
              /\ UNCHANGED << now, seq, unreadAt, quiet, hold, parked, 
                              happened, timer, jobs, asking, busy, worked, 
                              resume, viewing, hasTab, tabRev, lastStamp, 
                              tabView, knownRev, owed, settled, poppedParked, 
                              poppedSettled, poppedNoTab, staleFire, earlyEnd >>

Reload(self) == /\ TabPresence /\ <<tabView[self[2]], knownRev[self[2]]>> # <<hasTab, tabRev>>
                /\ tabView' = [tabView EXCEPT ![self[2]] = hasTab]
                /\ knownRev' = [knownRev EXCEPT ![self[2]] = tabRev]
                /\ UNCHANGED << now, seq, unreadAt, quiet, hold, parked, 
                                happened, timer, jobs, asking, busy, worked, 
                                resume, viewing, hasTab, tabRev, lastStamp, 
                                parkView, owed, settled, poppedParked, 
                                poppedSettled, poppedNoTab, staleFire, 
                                earlyEnd >>

Next == Person \/ Clock \/ TimerJobs \/ IdleEnds \/ Restores
           \/ (\E self \in {<<"rows", w>> : w \in Windows}: Rows(self))
           \/ (\E self \in {<<"tabs", w>> : w \in Windows}: Tabs(self))
           \/ (\E self \in {<<"reload", w>> : w \in Windows}: Reload(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Clock)
        /\ WF_vars(TimerJobs)
        /\ WF_vars(IdleEnds)
        /\ WF_vars(Restores)
        /\ \A self \in {<<"rows", w>> : w \in Windows} : WF_vars(Rows(self))
        /\ \A self \in {<<"tabs", w>> : w \in Windows} : WF_vars(Tabs(self))
        /\ \A self \in {<<"reload", w>> : w \in Windows} : WF_vars(Reload(self))

\* END TRANSLATION

---------------------------------------------------------------------------------
\* Requirements.

TypeOK ==
    /\ now \in 0..MaxTime
    /\ seq \in Nat /\ unreadAt \in 0..seq /\ settled \in 0..seq
    /\ {quiet, hold, parked, happened, asking, busy, worked, viewing, hasTab, owed, poppedParked, poppedSettled, poppedNoTab, staleFire, earlyEnd} \subseteq BOOLEAN
    /\ timer \in Timers
    /\ resume \in Timers \cup {Off}
    /\ jobs \subseteq Timers \ {None}
    /\ tabRev \in 0..2 * MaxTabMoves
    /\ lastStamp \in [Windows -> 0..seq]
    /\ parkView \in [Windows -> BOOLEAN]
    /\ tabView \in [Windows -> BOOLEAN]
    /\ knownRev \in [Windows -> 0..2 * MaxTabMoves]

\* 4: a parked session doesn't bother the person.
NoPopWhileParked == ~poppedParked

\* 4: what a park settled, and what happened during it, never pops up, not even after unparking.
\* Also: news on a tab the person closed never pops up, not even once they open it again.
NoPopSettled == ~poppedSettled

\* A tab the person does not have never pops up.
NoPopWithoutTab == ~poppedNoTab

\* 5: a cancelled or replaced timer never wakes the session.
NoStaleFire == ~staleFire

\* A timer only on a parked session: a park time, or a park until it is done.
OneTimer == timer # None => parked

\* A park until it is done never ends while its session still has work, unless a turn failed or the
\* agent asked.
DoneEndsOnlyWhenIdle == ~earlyEnd

\* Only a parked tab remembers no park of its own: the park a question ended waits on an unparked tab.
ResumeOnlyUnparked == resume # Off => ~parked \/ timer.kind = "park"

\* A closed tab is never parked until unparked: closing it ended that park.
NoUntimedParkClosed == (parked /\ timer = None) => hasTab

\* A mark held for the session in view only exists while viewing it.
HoldOnlyInView == hold => viewing

\* 1: anything that needs the person raises a pop-up, unless they handle it first.
NoLostPop == owed ~> ~owed

\* 5: a timed park ends.
ParkWakes == (parked /\ timer.kind = "park") ~> ~parked

\* A park until it is done ends once its session is idle after a turn ended, unless work starts again first.
IdleEndsDone == (parked /\ timer = Done /\ worked /\ ~busy) ~> (timer # Done \/ busy)

\* Once no question waits, the park a question ended comes back or is dropped, unless another question comes first.
QuestionRestores == (resume # Off /\ ~asking) ~> (resume = Off \/ asking)

\* A closed tab with a timer comes back. A park until it is done waits for a turn end, which may
\* never come; once one does, it is a park due at once, which this covers.
TimerBringsTabBack == (~hasTab /\ timer \notin {None, Done}) ~> hasTab

---------------------------------------------------------------------------------
\* What must happen in at least one state, which the .cfg files list under _POSSIBLE, so a check
\* that passes doesn't pass because the model never reaches a pop-up or a reopened tab.

\* A window has popped up: the mark it saw is loud, and no pop-up is still owed.
Pops == \E w \in Windows : lastStamp[w] # 0 /\ ~quiet /\ owed = FALSE /\ unreadAt = lastStamp[w] /\ seq > 0

\* The tab is open again after one close.
ClosedTabComesBack == hasTab /\ tabRev = 2

\* A question brought the closed tab back, its park ended.
QuestionBringsTabBack == hasTab /\ asking /\ tabRev = 2 /\ timer = None

\* A tab is parked until it is done.
ParkedUntilDone == parked /\ timer = Done

\* The server ends a park until it is done at idle (a step, not a state).
IdleEndsDonePark == timer = Done /\ worked /\ ~busy /\ ~busy' /\ timer'.kind = "park"

\* A failed turn ends a park until it is done while more work waits (a step).
FailureEndsDonePark == timer = Done /\ busy' /\ ~asking' /\ timer'.kind = "park"

\* The server puts back the park a question ended (a step).
ParkRestored == resume # Off /\ ~parked /\ parked'
=================================================================================
