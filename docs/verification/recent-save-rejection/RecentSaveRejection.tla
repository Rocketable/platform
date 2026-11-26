------------------------ MODULE RecentSaveRejection ------------------------
EXTENDS Integers, Sequences, FiniteSets

CONSTANT Rule

(* --algorithm RecentSaveRejection
variables issued = 0, pending = 0, queue = <<>>, active = 0,
          started = <<>>, detached = 0, reset = FALSE, overlap = FALSE;

define
    NewestStaysVisible ==
        (issued # 0 /\ (issued = active \/ issued = detached \/
            \E i \in 1..Len(queue): queue[i] = issued)) => pending = issued
    SavesStayOrdered == ~overlap /\ started = SubSeq(<<1, 2, 3>>, 1, Len(started))
end define;

begin
NextSave:
    while TRUE do
        either
            await issued < 3;
            issued := issued + 1;
            pending := issued;
            if reset then
                detached := issued;
                reset := FALSE;
            else
                queue := Append(queue, issued);
            end if;
        or
            await active = 0 /\ Len(queue) > 0;
            active := Head(queue);
            queue := Tail(queue);
            started := Append(started, active);
        or
            await detached # 0;
            overlap := active # 0;
            active := detached;
            detached := 0;
            started := Append(started, active);
        or
            await active # 0;
            if active = issued then pending := 0; end if;
            active := 0;
        or
            await active # 0;
            if active = issued \/ Rule = "clear" then pending := 0; end if;
            if Rule = "reset" /\ Len(queue) > 0 /\ issued < 3 then
                reset := TRUE;
            end if;
            active := 0;
        end either;
    end while;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "deb2ef16" /\ chksum(tla) = "8f4d8969")
VARIABLES issued, pending, queue, active, started, detached, reset, overlap

(* define statement *)
NewestStaysVisible ==
    (issued # 0 /\ (issued = active \/ issued = detached \/
        \E i \in 1..Len(queue): queue[i] = issued)) => pending = issued
SavesStayOrdered == ~overlap /\ started = SubSeq(<<1, 2, 3>>, 1, Len(started))


vars == << issued, pending, queue, active, started, detached, reset, overlap
        >>

Init == (* Global variables *)
        /\ issued = 0
        /\ pending = 0
        /\ queue = <<>>
        /\ active = 0
        /\ started = <<>>
        /\ detached = 0
        /\ reset = FALSE
        /\ overlap = FALSE

Next == \/ /\ issued < 3
           /\ issued' = issued + 1
           /\ pending' = issued'
           /\ IF reset
                 THEN /\ detached' = issued'
                      /\ reset' = FALSE
                      /\ queue' = queue
                 ELSE /\ queue' = Append(queue, issued')
                      /\ UNCHANGED << detached, reset >>
           /\ UNCHANGED <<active, started, overlap>>
        \/ /\ active = 0 /\ Len(queue) > 0
           /\ active' = Head(queue)
           /\ queue' = Tail(queue)
           /\ started' = Append(started, active')
           /\ UNCHANGED <<issued, pending, detached, reset, overlap>>
        \/ /\ detached # 0
           /\ overlap' = (active # 0)
           /\ active' = detached
           /\ detached' = 0
           /\ started' = Append(started, active')
           /\ UNCHANGED <<issued, pending, queue, reset>>
        \/ /\ active # 0
           /\ IF active = issued
                 THEN /\ pending' = 0
                 ELSE /\ TRUE
                      /\ UNCHANGED pending
           /\ active' = 0
           /\ UNCHANGED <<issued, queue, started, detached, reset, overlap>>
        \/ /\ active # 0
           /\ IF active = issued \/ Rule = "clear"
                 THEN /\ pending' = 0
                 ELSE /\ TRUE
                      /\ UNCHANGED pending
           /\ IF Rule = "reset" /\ Len(queue) > 0 /\ issued < 3
                 THEN /\ reset' = TRUE
                 ELSE /\ TRUE
                      /\ reset' = reset
           /\ active' = 0
           /\ UNCHANGED <<issued, queue, started, detached, overlap>>

Spec == Init /\ [][Next]_vars

\* END TRANSLATION 
=============================================================================
