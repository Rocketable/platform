--------------------------- MODULE VoiceRetryLimit ---------------------------
(* One Web voice call in the browser (internal/rocketclaw/web/src/voice.ts), from
   its first try to its end. A try is one call to OpenAI: it either goes live, or
   ends with a network loss. A live call can lose its connection too. After a
   loss the browser waits, then tries again on the same microphone.

   The outage clock starts at the loss that begins an outage: the first loss
   after the call was live, or the first try failing for a call never live yet.
   Time is counted in ticks and kept only up to Budget, as the code compares it
   with the limit only.

   - Limited: after each loss the browser ends the call when the outage has
     lasted Budget or more. FALSE is the code as built, which tries forever.
   - ResetOnOpen: a try that goes live clears the outage, so the next loss
     starts a new one. FALSE is a design that forgets to, used to show that
     NoEarlyStop catches it.

   The browser keeps working (weak fairness); the world, which loses live calls
   and hangs up, does not have to act. Whether a try goes live or fails is the
   world's choice in the browser's step, so tries may fail forever. *)
EXTENDS Naturals

CONSTANTS Budget, MaxLosses, Limited, ResetOnOpen

ASSUME Budget \in Nat \ {0} /\ MaxLosses \in Nat
ASSUME Limited \in BOOLEAN /\ ResetOnOpen \in BOOLEAN

Min(a, b) == IF a < b THEN a ELSE b

(* --algorithm VoiceRetryLimit
variables
    state = "trying",
    ticking = FALSE,
    elapsed = 0,
    losses = 0,
    inOutage = FALSE,
    outageTime = 0,
    failures = 0,
    reconnected = FALSE;

define
    Calling == {"live", "lost", "waiting", "trying"}
    Retrying == {"lost", "waiting", "trying"}

    TypeOK ==
        /\ state \in Calling \cup {"stopped", "hungup"}
        /\ ticking \in BOOLEAN
        /\ elapsed \in 0..Budget
        /\ losses \in 0..MaxLosses
        /\ inOutage \in BOOLEAN
        /\ outageTime \in 0..Budget
        /\ failures \in 0..2
        /\ reconnected \in BOOLEAN

    NoEarlyStop == state = "stopped" => inOutage /\ outageTime >= Budget

    BoundedRetries == state = "waiting" => outageTime < Budget

    OutageEnds == (state \in Retrying) ~> (state \in {"live", "stopped", "hungup"})

    NeverReconnectAfterFailures == ~(state = "live" /\ failures >= 2)
    NeverStopped == state # "stopped"
    NeverStopAfterReconnect == ~(state = "stopped" /\ reconnected)
end define;

fair process browser = "browser"
begin
Loop:
    while TRUE do
        either
            await state = "trying";
            either
                state := "live";
                reconnected := reconnected \/ inOutage;
                inOutage := FALSE;
                if ResetOnOpen then
                    ticking := FALSE;
                end if;
            or
                with took \in 0..1 do
                    state := "lost";
                    elapsed := Min(elapsed + took, Budget);
                    if inOutage then
                        outageTime := Min(outageTime + took, Budget);
                        failures := Min(failures + 1, 2);
                    else
                        inOutage := TRUE;
                        outageTime := 0;
                        failures := 0;
                    end if;
                end with;
            end either;
        or
            await state = "lost";
            with start = IF ticking THEN elapsed ELSE 0 do
                ticking := TRUE;
                elapsed := start;
                if Limited /\ start >= Budget then
                    state := "stopped";
                else
                    state := "waiting";
                end if;
            end with;
        or
            await state = "waiting";
            state := "trying";
            elapsed := Min(elapsed + 1, Budget);
            outageTime := Min(outageTime + 1, Budget);
        end either;
    end while;
end process;

process world = "world"
begin
Act:
    while TRUE do
        either
            await state = "live" /\ losses < MaxLosses;
            losses := losses + 1;
            state := "lost";
            inOutage := TRUE;
            outageTime := 0;
            failures := 0;
        or
            await state \in Calling;
            state := "hungup";
        end either;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES state, ticking, elapsed, losses, inOutage, outageTime, failures, 
          reconnected

(* define statement *)
Calling == {"live", "lost", "waiting", "trying"}
Retrying == {"lost", "waiting", "trying"}

TypeOK ==
    /\ state \in Calling \cup {"stopped", "hungup"}
    /\ ticking \in BOOLEAN
    /\ elapsed \in 0..Budget
    /\ losses \in 0..MaxLosses
    /\ inOutage \in BOOLEAN
    /\ outageTime \in 0..Budget
    /\ failures \in 0..2
    /\ reconnected \in BOOLEAN

NoEarlyStop == state = "stopped" => inOutage /\ outageTime >= Budget

BoundedRetries == state = "waiting" => outageTime < Budget

OutageEnds == (state \in Retrying) ~> (state \in {"live", "stopped", "hungup"})

NeverReconnectAfterFailures == ~(state = "live" /\ failures >= 2)
NeverStopped == state # "stopped"
NeverStopAfterReconnect == ~(state = "stopped" /\ reconnected)


vars == << state, ticking, elapsed, losses, inOutage, outageTime, failures, 
           reconnected >>

ProcSet == {"browser"} \cup {"world"}

Init == (* Global variables *)
        /\ state = "trying"
        /\ ticking = FALSE
        /\ elapsed = 0
        /\ losses = 0
        /\ inOutage = FALSE
        /\ outageTime = 0
        /\ failures = 0
        /\ reconnected = FALSE

browser == /\ \/ /\ state = "trying"
                 /\ \/ /\ state' = "live"
                       /\ reconnected' = (reconnected \/ inOutage)
                       /\ inOutage' = FALSE
                       /\ IF ResetOnOpen
                             THEN /\ ticking' = FALSE
                             ELSE /\ TRUE
                                  /\ UNCHANGED ticking
                       /\ UNCHANGED <<elapsed, outageTime, failures>>
                    \/ /\ \E took \in 0..1:
                            /\ state' = "lost"
                            /\ elapsed' = Min(elapsed + took, Budget)
                            /\ IF inOutage
                                  THEN /\ outageTime' = Min(outageTime + took, Budget)
                                       /\ failures' = Min(failures + 1, 2)
                                       /\ UNCHANGED inOutage
                                  ELSE /\ inOutage' = TRUE
                                       /\ outageTime' = 0
                                       /\ failures' = 0
                       /\ UNCHANGED <<ticking, reconnected>>
              \/ /\ state = "lost"
                 /\ LET start == IF ticking THEN elapsed ELSE 0 IN
                      /\ ticking' = TRUE
                      /\ elapsed' = start
                      /\ IF Limited /\ start >= Budget
                            THEN /\ state' = "stopped"
                            ELSE /\ state' = "waiting"
                 /\ UNCHANGED <<inOutage, outageTime, failures, reconnected>>
              \/ /\ state = "waiting"
                 /\ state' = "trying"
                 /\ elapsed' = Min(elapsed + 1, Budget)
                 /\ outageTime' = Min(outageTime + 1, Budget)
                 /\ UNCHANGED <<ticking, inOutage, failures, reconnected>>
           /\ UNCHANGED losses

world == /\ \/ /\ state = "live" /\ losses < MaxLosses
               /\ losses' = losses + 1
               /\ state' = "lost"
               /\ inOutage' = TRUE
               /\ outageTime' = 0
               /\ failures' = 0
            \/ /\ state \in Calling
               /\ state' = "hungup"
               /\ UNCHANGED <<losses, inOutage, outageTime, failures>>
         /\ UNCHANGED << ticking, elapsed, reconnected >>

Next == browser \/ world

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(browser)

\* END TRANSLATION
==============================================================================
