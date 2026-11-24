---- MODULE OrderedLog ----
EXTENDS Integers, Sequences, TLC

CONSTANTS MaxSends, Replay, Loss, MaxDrops

(* --algorithm OrderedLog
variables
    log = <<>>,
    ocBusy = FALSE,
    inflight = FALSE,
    sends = 0,
    parked = FALSE,
    parkFor = 0,
    parkSeen = FALSE,
    connected = TRUE,
    cursor = 0,
    drops = 0,
    browserBell = FALSE,
    screenParked = FALSE,
    earlyEnd = FALSE;

define
    Done == pc["relay"] = "Done" /\ ~inflight /\ ~ocBusy
    PromptPending == pc["relay"] = "Proxy" \/ inflight \/ ocBusy
    NoEarlyEnd == ~earlyEnd
    ScreenCatchesUp == Done ~> ~screenParked
end define;

fair process Relay = "relay"
begin
Send:
    while sends < MaxSends do
Park:
        parked := TRUE || parkFor := sends + 1 || parkSeen := FALSE || sends := sends + 1;
        browserBell := TRUE;
Proxy:
        inflight := TRUE;
Respond:
        await ~inflight;
    end while;
end process;

fair process OpenCode = "opencode"
begin
Serve:
    while TRUE do
        either
            await inflight;
            inflight := FALSE || ocBusy := TRUE || log := Append(log, [k |-> "admit", p |-> sends]);
        or
            await ocBusy;
            ocBusy := FALSE || log := Append(log, [k |-> "end", p |-> 0]);
        end either;
    end while;
end process;

fair process Follow = "follow"
begin
Apply:
    while TRUE do
        await connected /\ cursor < Len(log);
        if log[cursor + 1].k = "admit" /\ log[cursor + 1].p = parkFor then
            parkSeen := TRUE;
        elsif log[cursor + 1].k = "end" /\ parked /\ parkSeen then
            earlyEnd := earlyEnd \/ PromptPending;
            parked := FALSE;
            browserBell := TRUE;
        end if;
        cursor := cursor + 1;
    end while;
end process;

fair process Net = "net"
begin
Cut:
    while Loss /\ drops < MaxDrops do
        connected := FALSE || drops := drops + 1;
Rejoin:
        connected := TRUE;
        if ~Replay then
            cursor := Len(log);
        end if;
    end while;
end process;

fair process Browser = "browser"
begin
Fetch:
    while TRUE do
        either
            await browserBell;
            browserBell := FALSE;
        or
            skip;
        end either;
Read:
        screenParked := parked;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, log, ocBusy, inflight, sends, parked, parkFor, parkSeen, 
          connected, cursor, drops, browserBell, screenParked, earlyEnd

(* define statement *)
Done == pc["relay"] = "Done" /\ ~inflight /\ ~ocBusy
PromptPending == pc["relay"] = "Proxy" \/ inflight \/ ocBusy
NoEarlyEnd == ~earlyEnd
ScreenCatchesUp == Done ~> ~screenParked


vars == << pc, log, ocBusy, inflight, sends, parked, parkFor, parkSeen, 
           connected, cursor, drops, browserBell, screenParked, earlyEnd >>

ProcSet == {"relay"} \cup {"opencode"} \cup {"follow"} \cup {"net"} \cup {"browser"}

Init == (* Global variables *)
        /\ log = <<>>
        /\ ocBusy = FALSE
        /\ inflight = FALSE
        /\ sends = 0
        /\ parked = FALSE
        /\ parkFor = 0
        /\ parkSeen = FALSE
        /\ connected = TRUE
        /\ cursor = 0
        /\ drops = 0
        /\ browserBell = FALSE
        /\ screenParked = FALSE
        /\ earlyEnd = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "relay" -> "Send"
                                        [] self = "opencode" -> "Serve"
                                        [] self = "follow" -> "Apply"
                                        [] self = "net" -> "Cut"
                                        [] self = "browser" -> "Fetch"]

Send == /\ pc["relay"] = "Send"
        /\ IF sends < MaxSends
              THEN /\ pc' = [pc EXCEPT !["relay"] = "Park"]
              ELSE /\ pc' = [pc EXCEPT !["relay"] = "Done"]
        /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, 
                        parkSeen, connected, cursor, drops, browserBell, 
                        screenParked, earlyEnd >>

Park == /\ pc["relay"] = "Park"
        /\ /\ parkFor' = sends + 1
           /\ parkSeen' = FALSE
           /\ parked' = TRUE
           /\ sends' = sends + 1
        /\ browserBell' = TRUE
        /\ pc' = [pc EXCEPT !["relay"] = "Proxy"]
        /\ UNCHANGED << log, ocBusy, inflight, connected, cursor, drops, 
                        screenParked, earlyEnd >>

Proxy == /\ pc["relay"] = "Proxy"
         /\ inflight' = TRUE
         /\ pc' = [pc EXCEPT !["relay"] = "Respond"]
         /\ UNCHANGED << log, ocBusy, sends, parked, parkFor, parkSeen, 
                         connected, cursor, drops, browserBell, screenParked, 
                         earlyEnd >>

Respond == /\ pc["relay"] = "Respond"
           /\ ~inflight
           /\ pc' = [pc EXCEPT !["relay"] = "Send"]
           /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, 
                           parkSeen, connected, cursor, drops, browserBell, 
                           screenParked, earlyEnd >>

Relay == Send \/ Park \/ Proxy \/ Respond

Serve == /\ pc["opencode"] = "Serve"
         /\ \/ /\ inflight
               /\ /\ inflight' = FALSE
                  /\ log' = Append(log, [k |-> "admit", p |-> sends])
                  /\ ocBusy' = TRUE
            \/ /\ ocBusy
               /\ /\ log' = Append(log, [k |-> "end", p |-> 0])
                  /\ ocBusy' = FALSE
               /\ UNCHANGED inflight
         /\ pc' = [pc EXCEPT !["opencode"] = "Serve"]
         /\ UNCHANGED << sends, parked, parkFor, parkSeen, connected, cursor, 
                         drops, browserBell, screenParked, earlyEnd >>

OpenCode == Serve

Apply == /\ pc["follow"] = "Apply"
         /\ connected /\ cursor < Len(log)
         /\ IF log[cursor + 1].k = "admit" /\ log[cursor + 1].p = parkFor
               THEN /\ parkSeen' = TRUE
                    /\ UNCHANGED << parked, browserBell, earlyEnd >>
               ELSE /\ IF log[cursor + 1].k = "end" /\ parked /\ parkSeen
                          THEN /\ earlyEnd' = (earlyEnd \/ PromptPending)
                               /\ parked' = FALSE
                               /\ browserBell' = TRUE
                          ELSE /\ TRUE
                               /\ UNCHANGED << parked, browserBell, earlyEnd >>
                    /\ UNCHANGED parkSeen
         /\ cursor' = cursor + 1
         /\ pc' = [pc EXCEPT !["follow"] = "Apply"]
         /\ UNCHANGED << log, ocBusy, inflight, sends, parkFor, connected, 
                         drops, screenParked >>

Follow == Apply

Cut == /\ pc["net"] = "Cut"
       /\ IF Loss /\ drops < MaxDrops
             THEN /\ /\ connected' = FALSE
                     /\ drops' = drops + 1
                  /\ pc' = [pc EXCEPT !["net"] = "Rejoin"]
             ELSE /\ pc' = [pc EXCEPT !["net"] = "Done"]
                  /\ UNCHANGED << connected, drops >>
       /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, parkSeen, 
                       cursor, browserBell, screenParked, earlyEnd >>

Rejoin == /\ pc["net"] = "Rejoin"
          /\ connected' = TRUE
          /\ IF ~Replay
                THEN /\ cursor' = Len(log)
                ELSE /\ TRUE
                     /\ UNCHANGED cursor
          /\ pc' = [pc EXCEPT !["net"] = "Cut"]
          /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, 
                          parkSeen, drops, browserBell, screenParked, earlyEnd >>

Net == Cut \/ Rejoin

Fetch == /\ pc["browser"] = "Fetch"
         /\ \/ /\ browserBell
               /\ browserBell' = FALSE
            \/ /\ TRUE
               /\ UNCHANGED browserBell
         /\ pc' = [pc EXCEPT !["browser"] = "Read"]
         /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, 
                         parkSeen, connected, cursor, drops, screenParked, 
                         earlyEnd >>

Read == /\ pc["browser"] = "Read"
        /\ screenParked' = parked
        /\ pc' = [pc EXCEPT !["browser"] = "Fetch"]
        /\ UNCHANGED << log, ocBusy, inflight, sends, parked, parkFor, 
                        parkSeen, connected, cursor, drops, browserBell, 
                        earlyEnd >>

Browser == Fetch \/ Read

Next == Relay \/ OpenCode \/ Follow \/ Net \/ Browser

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Relay)
        /\ WF_vars(OpenCode)
        /\ WF_vars(Follow)
        /\ WF_vars(Net)
        /\ WF_vars(Browser)

\* END TRANSLATION
====
