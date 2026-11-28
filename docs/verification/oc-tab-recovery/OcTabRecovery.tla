---------------------------- MODULE OcTabRecovery ----------------------------
EXTENDS Naturals

CONSTANTS FastFail, ServerError, StreamSurvives, KeepList, RetryTranscript, TransportAsTimeout, MetaUnreachable, MaxOutages

(* --algorithm OcTabRecovery
variables
    rcDown = FALSE, ocDown = FALSE, outages = 0, dropped = FALSE,
    list = "present", mounted = TRUE, conn = "connected", health = "ok",
    opened = FALSE,
    res = "idle", resMetaCur = FALSE, resMetaFollow = FALSE, resMetaDone = FALSE,
    resMsgCur = FALSE, resMsgFollow = FALSE, resMsgFailed = FALSE, resMetaRetry = FALSE, served = FALSE,
    metaCached = FALSE, loaded = FALSE,
    mE = "none", mK = "ok", mI = FALSE, mF = FALSE,
    tE = "none", tK = "ok", tI = FALSE, tF = FALSE,
    timeline = "idle", tlCur = FALSE, tlFollow = FALSE;

define
    Outage == rcDown \/ ocDown
    Kinds == IF Outage THEN (IF FastFail THEN {"fail", "stall"} ELSE {"stall"})
             ELSE IF ServerError /\ ~served THEN {"ok", "error"} ELSE {"ok"}
    Fast(k) == k \in {"ok", "fail", "error"}
    Flush == mounted /\ res = "ready" /\ timeline = "idle"
    Settled == opened /\ ~Outage /\ outages = MaxOutages
    Good == list = "present" /\ mounted /\ res = "ready" /\ loaded /\ timeline \in {"syncing", "ready"}
    Blank == list = "present" /\ mounted
             /\ \/ res = "idle"
                \/ res = "resolving" /\ health = "ok"
                \/ res = "ready" /\ ~loaded /\ timeline # "failed"
    Empty == list = "present" /\ mounted /\ (res = "idle" \/ (res = "resolving" /\ health = "ok"))
    Recovers == Settled ~> Good
    ErrorShown == list = "present" /\ mounted /\ (res = "error" \/ (res = "ready" /\ timeline = "failed"))
    ServerErrorVisible == (served /\ Settled) ~> (Good \/ ErrorShown)
    NoStuckBlank == Settled ~> ~Blank
    NoStuckEmpty == Settled ~> ~Empty
end define;

macro LoadMeta() begin
    if mE = "none" then
        with k \in Kinds do mE := "pending"; mK := k; mI := FALSE; mF := FALSE; end with;
    elsif mE = "pending" /\ mI /\ ~mF then
        mF := TRUE;
    end if;
    resMetaDone := mE = "done";
    resMetaCur := mE = "none" \/ (mE = "pending" /\ ~mI /\ ~mF);
    resMetaFollow := mE = "pending" /\ (mI \/ mF);
end macro;

macro LoadMsgs(isTimeline) begin
    if tE = "none" then
        with k \in Kinds do tE := "pending"; tK := k; tI := FALSE; tF := FALSE; end with;
    elsif tE = "pending" /\ tI /\ ~tF then
        tF := TRUE;
    end if;
    if isTimeline then
        timeline := IF tE = "done" THEN "ready" ELSE "syncing";
        tlCur := tE = "none" \/ (tE = "pending" /\ ~tI /\ ~tF);
        tlFollow := tE = "pending" /\ (tI \/ tF);
    else
        resMsgCur := tE = "none" \/ (tE = "pending" /\ ~tI /\ ~tF);
        resMsgFollow := tE = "pending" /\ (tI \/ tF);
    end if;
end macro;

macro Dispose() begin
    mounted := FALSE; conn := "connecting"; health := "ok";
    res := "idle"; resMetaCur := FALSE; resMetaFollow := FALSE; resMetaDone := FALSE;
    resMsgCur := FALSE; resMsgFollow := FALSE; resMsgFailed := FALSE; resMetaRetry := FALSE;
    metaCached := FALSE; loaded := FALSE;
    mE := "none"; mI := FALSE; mF := FALSE; tE := "none"; tI := FALSE; tF := FALSE;
    timeline := "idle"; tlCur := FALSE; tlFollow := FALSE;
end macro;

fair process Person = "person"
begin
    Open:
        await ~Flush;
        opened := TRUE;
        if mounted then
            res := IF metaCached THEN "ready" ELSE "resolving";
            if conn = "connected" then
                LoadMeta();
                LoadMsgs(FALSE);
            end if;
        end if;
end process;

fair process Network = "network"
begin
    Weather:
        while TRUE do
            await ~Flush;
            either
                await ~Outage /\ outages < MaxOutages;
                with which \in {"rc", "oc", "both"} do
                    rcDown := which # "oc"; ocDown := which # "rc";
                end with;
                outages := outages + 1; dropped := FALSE;
                with k \in {"ok", "stall"} \cup (IF FastFail THEN {"fail"} ELSE {}),
                     j \in {"ok", "stall"} \cup (IF FastFail THEN {"fail"} ELSE {}) do
                    if mE = "pending" then mK := k; end if;
                    if tE = "pending" then tK := j; end if;
                end with;
            or
                await Outage /\ (StreamSurvives \/ dropped \/ ~mounted \/ conn # "connected");
                rcDown := FALSE; ocDown := FALSE;
            end either;
        end while;
end process;

fair process RocketClaw = "rocketclaw"
begin
    Identity:
        while TRUE do
            await ~Flush;
            either
                await rcDown /\ list = "present" /\ ~KeepList;
                list := "absent";
                Dispose();
            or
                await ~rcDown /\ list = "absent";
                list := "present"; mounted := TRUE; conn := "connecting";
                res := IF opened THEN "resolving" ELSE "idle";
            end either;
        end while;
end process;

fair process Connection = "connection"
begin
    Stream:
        while TRUE do
            await ~Flush /\ mounted;
            either
                await conn # "connected" /\ ~Outage;
                conn := "connected";
                if res \in {"resolving", "unreachable", "error"} then
                    res := "resolving";
                    LoadMeta();
                    LoadMsgs(FALSE);
                elsif res = "ready" then
                    LoadMeta();
                    LoadMsgs(FALSE);
                end if;
            or
                await conn = "connected" /\ Outage;
                conn := "reconnecting"; dropped := TRUE;
                if mE = "done" then mE := "none"; elsif mE = "pending" /\ ~mF then mI := TRUE; end if;
                if tE = "done" then tE := "none"; elsif tE = "pending" /\ ~tF then tI := TRUE; end if;
            end either;
        end while;
end process;

fair process Health = "health"
begin
    Probe:
        while TRUE do
            await ~Flush /\ mounted;
            either
                await Outage /\ health = "ok";
                health := "unreachable";
            or
                await ~Outage /\ health = "unreachable";
                health := "ok";
            end either;
        end while;
end process;

fair process MetaServer = "meta"
variables mOk = FALSE, mQueued = FALSE;
begin
    SettleMeta:
        while TRUE do
            await ~Flush /\ mounted /\ mE = "pending" /\ (mK = "stall" => ~(res = "resolving" /\ resMetaCur));
            mOk := mK = "ok";
            mQueued := mF;
            metaCached := metaCached \/ mOk;
            res := IF mOk /\ res # "idle" THEN "ready"
                   ELSE IF resMetaCur /\ res \in {"resolving", "unreachable"}
                        THEN (IF MetaUnreachable /\ mK # "error" THEN "unreachable" ELSE "error")
                   ELSE res;
            resMetaRetry := MetaUnreachable /\ ~mOk /\ mK # "error" /\ resMetaCur /\ res \in {"resolving", "unreachable"};
            served := served \/ mK = "error";
            if mQueued then
                with k \in Kinds do mK := k; mI := FALSE; mF := FALSE; end with;
            else
                mE := IF mOk /\ ~mI THEN "done" ELSE "none";
            end if;
            resMetaCur := mQueued /\ resMetaFollow;
            resMetaFollow := FALSE;
        end while;
end process;

fair process MsgServer = "msgs"
variables tOk = FALSE, tQueued = FALSE, tHit = FALSE;
begin
    SettleMsgs:
        while TRUE do
            await ~Flush /\ mounted /\ tE = "pending" /\ (tK = "stall" => ~(timeline = "syncing" /\ tlCur));
            tOk := tK = "ok";
            tQueued := tF;
            tHit := timeline = "syncing" /\ tlCur;
            loaded := loaded \/ tOk;
            timeline := IF tHit THEN (IF tOk \/ (TransportAsTimeout /\ tK # "error") THEN "ready" ELSE "failed") ELSE timeline;
            resMsgFailed := IF resMsgCur THEN ~tOk /\ tK # "error" ELSE resMsgFailed;
            served := served \/ tK = "error";
            if tQueued then
                with k \in Kinds do tK := k; tI := FALSE; tF := FALSE; end with;
            else
                tE := IF tOk /\ ~tI THEN "done" ELSE "none";
            end if;
            tlCur := tQueued /\ tlFollow;
            tlFollow := FALSE;
            resMsgCur := tQueued /\ resMsgFollow;
            resMsgFollow := FALSE;
        end while;
end process;

fair process TimelineEffect = "timeline"
begin
    Start:
        while TRUE do
            await Flush;
            LoadMsgs(TRUE);
        end while;
end process;

fair process MetaDone = "metadone"
begin
    Cached:
        while TRUE do
            await ~Flush /\ mounted /\ resMetaDone;
            resMetaDone := FALSE;
            if res \in {"resolving", "unreachable"} then res := "ready"; end if;
        end while;
end process;

fair process ResolutionTimer = "restimer"
begin
    ResolveTimeout:
        while TRUE do
            await ~Flush /\ mounted /\ res = "resolving" /\ ~(resMetaCur /\ mE = "pending" /\ Fast(mK));
            res := "unreachable";
        end while;
end process;

fair process TimelineTimer = "tltimer"
begin
    SyncTimeout:
        while TRUE do
            await ~Flush /\ mounted /\ timeline = "syncing" /\ ~(tlCur /\ tE = "pending" /\ Fast(tK));
            timeline := "ready"; tlCur := FALSE; tlFollow := FALSE;
        end while;
end process;

fair process Retry = "retry"
begin
    Again:
        while TRUE do
            await ~Flush /\ mounted /\ RetryTranscript /\ resMsgFailed /\ res # "idle";
            resMsgFailed := FALSE;
            LoadMsgs(FALSE);
        end while;
end process;
fair process MetaRetry = "metaretry"
begin
    MetaAgain:
        while TRUE do
            await ~Flush /\ mounted /\ resMetaRetry /\ res \in {"resolving", "unreachable"};
            resMetaRetry := FALSE;
            LoadMeta();
        end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, rcDown, ocDown, outages, dropped, list, mounted, conn, health, 
          opened, res, resMetaCur, resMetaFollow, resMetaDone, resMsgCur, 
          resMsgFollow, resMsgFailed, resMetaRetry, served, metaCached, 
          loaded, mE, mK, mI, mF, tE, tK, tI, tF, timeline, tlCur, tlFollow

(* define statement *)
Outage == rcDown \/ ocDown
Kinds == IF Outage THEN (IF FastFail THEN {"fail", "stall"} ELSE {"stall"})
         ELSE IF ServerError /\ ~served THEN {"ok", "error"} ELSE {"ok"}
Fast(k) == k \in {"ok", "fail", "error"}
Flush == mounted /\ res = "ready" /\ timeline = "idle"
Settled == opened /\ ~Outage /\ outages = MaxOutages
Good == list = "present" /\ mounted /\ res = "ready" /\ loaded /\ timeline \in {"syncing", "ready"}
Blank == list = "present" /\ mounted
         /\ \/ res = "idle"
            \/ res = "resolving" /\ health = "ok"
            \/ res = "ready" /\ ~loaded /\ timeline # "failed"
Empty == list = "present" /\ mounted /\ (res = "idle" \/ (res = "resolving" /\ health = "ok"))
Recovers == Settled ~> Good
ErrorShown == list = "present" /\ mounted /\ (res = "error" \/ (res = "ready" /\ timeline = "failed"))
ServerErrorVisible == (served /\ Settled) ~> (Good \/ ErrorShown)
NoStuckBlank == Settled ~> ~Blank
NoStuckEmpty == Settled ~> ~Empty

VARIABLES mOk, mQueued, tOk, tQueued, tHit

vars == << pc, rcDown, ocDown, outages, dropped, list, mounted, conn, health, 
           opened, res, resMetaCur, resMetaFollow, resMetaDone, resMsgCur, 
           resMsgFollow, resMsgFailed, resMetaRetry, served, metaCached, 
           loaded, mE, mK, mI, mF, tE, tK, tI, tF, timeline, tlCur, tlFollow, 
           mOk, mQueued, tOk, tQueued, tHit >>

ProcSet == {"person"} \cup {"network"} \cup {"rocketclaw"} \cup {"connection"} \cup {"health"} \cup {"meta"} \cup {"msgs"} \cup {"timeline"} \cup {"metadone"} \cup {"restimer"} \cup {"tltimer"} \cup {"retry"} \cup {"metaretry"}

Init == (* Global variables *)
        /\ rcDown = FALSE
        /\ ocDown = FALSE
        /\ outages = 0
        /\ dropped = FALSE
        /\ list = "present"
        /\ mounted = TRUE
        /\ conn = "connected"
        /\ health = "ok"
        /\ opened = FALSE
        /\ res = "idle"
        /\ resMetaCur = FALSE
        /\ resMetaFollow = FALSE
        /\ resMetaDone = FALSE
        /\ resMsgCur = FALSE
        /\ resMsgFollow = FALSE
        /\ resMsgFailed = FALSE
        /\ resMetaRetry = FALSE
        /\ served = FALSE
        /\ metaCached = FALSE
        /\ loaded = FALSE
        /\ mE = "none"
        /\ mK = "ok"
        /\ mI = FALSE
        /\ mF = FALSE
        /\ tE = "none"
        /\ tK = "ok"
        /\ tI = FALSE
        /\ tF = FALSE
        /\ timeline = "idle"
        /\ tlCur = FALSE
        /\ tlFollow = FALSE
        (* Process MetaServer *)
        /\ mOk = FALSE
        /\ mQueued = FALSE
        (* Process MsgServer *)
        /\ tOk = FALSE
        /\ tQueued = FALSE
        /\ tHit = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "person" -> "Open"
                                        [] self = "network" -> "Weather"
                                        [] self = "rocketclaw" -> "Identity"
                                        [] self = "connection" -> "Stream"
                                        [] self = "health" -> "Probe"
                                        [] self = "meta" -> "SettleMeta"
                                        [] self = "msgs" -> "SettleMsgs"
                                        [] self = "timeline" -> "Start"
                                        [] self = "metadone" -> "Cached"
                                        [] self = "restimer" -> "ResolveTimeout"
                                        [] self = "tltimer" -> "SyncTimeout"
                                        [] self = "retry" -> "Again"
                                        [] self = "metaretry" -> "MetaAgain"]

Open == /\ pc["person"] = "Open"
        /\ ~Flush
        /\ opened' = TRUE
        /\ IF mounted
              THEN /\ res' = IF metaCached THEN "ready" ELSE "resolving"
                   /\ IF conn = "connected"
                         THEN /\ IF mE = "none"
                                    THEN /\ \E k \in Kinds:
                                              /\ mE' = "pending"
                                              /\ mK' = k
                                              /\ mI' = FALSE
                                              /\ mF' = FALSE
                                    ELSE /\ IF mE = "pending" /\ mI /\ ~mF
                                               THEN /\ mF' = TRUE
                                               ELSE /\ TRUE
                                                    /\ mF' = mF
                                         /\ UNCHANGED << mE, mK, mI >>
                              /\ resMetaDone' = (mE' = "done")
                              /\ resMetaCur' = (mE' = "none" \/ (mE' = "pending" /\ ~mI' /\ ~mF'))
                              /\ resMetaFollow' = (mE' = "pending" /\ (mI' \/ mF'))
                              /\ IF tE = "none"
                                    THEN /\ \E k \in Kinds:
                                              /\ tE' = "pending"
                                              /\ tK' = k
                                              /\ tI' = FALSE
                                              /\ tF' = FALSE
                                    ELSE /\ IF tE = "pending" /\ tI /\ ~tF
                                               THEN /\ tF' = TRUE
                                               ELSE /\ TRUE
                                                    /\ tF' = tF
                                         /\ UNCHANGED << tE, tK, tI >>
                              /\ IF FALSE
                                    THEN /\ timeline' = (IF tE' = "done" THEN "ready" ELSE "syncing")
                                         /\ tlCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                         /\ tlFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                         /\ UNCHANGED << resMsgCur, 
                                                         resMsgFollow >>
                                    ELSE /\ resMsgCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                         /\ resMsgFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                         /\ UNCHANGED << timeline, tlCur, 
                                                         tlFollow >>
                         ELSE /\ TRUE
                              /\ UNCHANGED << resMetaCur, resMetaFollow, 
                                              resMetaDone, resMsgCur, 
                                              resMsgFollow, mE, mK, mI, mF, tE, 
                                              tK, tI, tF, timeline, tlCur, 
                                              tlFollow >>
              ELSE /\ TRUE
                   /\ UNCHANGED << res, resMetaCur, resMetaFollow, resMetaDone, 
                                   resMsgCur, resMsgFollow, mE, mK, mI, mF, tE, 
                                   tK, tI, tF, timeline, tlCur, tlFollow >>
        /\ pc' = [pc EXCEPT !["person"] = "Done"]
        /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, conn, 
                        health, resMsgFailed, resMetaRetry, served, metaCached, 
                        loaded, mOk, mQueued, tOk, tQueued, tHit >>

Person == Open

Weather == /\ pc["network"] = "Weather"
           /\ ~Flush
           /\ \/ /\ ~Outage /\ outages < MaxOutages
                 /\ \E which \in {"rc", "oc", "both"}:
                      /\ rcDown' = (which # "oc")
                      /\ ocDown' = (which # "rc")
                 /\ outages' = outages + 1
                 /\ dropped' = FALSE
                 /\ \E k \in {"ok", "stall"} \cup (IF FastFail THEN {"fail"} ELSE {}):
                      \E j \in {"ok", "stall"} \cup (IF FastFail THEN {"fail"} ELSE {}):
                        /\ IF mE = "pending"
                              THEN /\ mK' = k
                              ELSE /\ TRUE
                                   /\ mK' = mK
                        /\ IF tE = "pending"
                              THEN /\ tK' = j
                              ELSE /\ TRUE
                                   /\ tK' = tK
              \/ /\ Outage /\ (StreamSurvives \/ dropped \/ ~mounted \/ conn # "connected")
                 /\ rcDown' = FALSE
                 /\ ocDown' = FALSE
                 /\ UNCHANGED <<outages, dropped, mK, tK>>
           /\ pc' = [pc EXCEPT !["network"] = "Weather"]
           /\ UNCHANGED << list, mounted, conn, health, opened, res, 
                           resMetaCur, resMetaFollow, resMetaDone, resMsgCur, 
                           resMsgFollow, resMsgFailed, resMetaRetry, served, 
                           metaCached, loaded, mE, mI, mF, tE, tI, tF, 
                           timeline, tlCur, tlFollow, mOk, mQueued, tOk, 
                           tQueued, tHit >>

Network == Weather

Identity == /\ pc["rocketclaw"] = "Identity"
            /\ ~Flush
            /\ \/ /\ rcDown /\ list = "present" /\ ~KeepList
                  /\ list' = "absent"
                  /\ mounted' = FALSE
                  /\ conn' = "connecting"
                  /\ health' = "ok"
                  /\ res' = "idle"
                  /\ resMetaCur' = FALSE
                  /\ resMetaFollow' = FALSE
                  /\ resMetaDone' = FALSE
                  /\ resMsgCur' = FALSE
                  /\ resMsgFollow' = FALSE
                  /\ resMsgFailed' = FALSE
                  /\ resMetaRetry' = FALSE
                  /\ metaCached' = FALSE
                  /\ loaded' = FALSE
                  /\ mE' = "none"
                  /\ mI' = FALSE
                  /\ mF' = FALSE
                  /\ tE' = "none"
                  /\ tI' = FALSE
                  /\ tF' = FALSE
                  /\ timeline' = "idle"
                  /\ tlCur' = FALSE
                  /\ tlFollow' = FALSE
               \/ /\ ~rcDown /\ list = "absent"
                  /\ list' = "present"
                  /\ mounted' = TRUE
                  /\ conn' = "connecting"
                  /\ res' = IF opened THEN "resolving" ELSE "idle"
                  /\ UNCHANGED <<health, resMetaCur, resMetaFollow, resMetaDone, resMsgCur, resMsgFollow, resMsgFailed, resMetaRetry, metaCached, loaded, mE, mI, mF, tE, tI, tF, timeline, tlCur, tlFollow>>
            /\ pc' = [pc EXCEPT !["rocketclaw"] = "Identity"]
            /\ UNCHANGED << rcDown, ocDown, outages, dropped, opened, served, 
                            mK, tK, mOk, mQueued, tOk, tQueued, tHit >>

RocketClaw == Identity

Stream == /\ pc["connection"] = "Stream"
          /\ ~Flush /\ mounted
          /\ \/ /\ conn # "connected" /\ ~Outage
                /\ conn' = "connected"
                /\ IF res \in {"resolving", "unreachable", "error"}
                      THEN /\ res' = "resolving"
                           /\ IF mE = "none"
                                 THEN /\ \E k \in Kinds:
                                           /\ mE' = "pending"
                                           /\ mK' = k
                                           /\ mI' = FALSE
                                           /\ mF' = FALSE
                                 ELSE /\ IF mE = "pending" /\ mI /\ ~mF
                                            THEN /\ mF' = TRUE
                                            ELSE /\ TRUE
                                                 /\ mF' = mF
                                      /\ UNCHANGED << mE, mK, mI >>
                           /\ resMetaDone' = (mE' = "done")
                           /\ resMetaCur' = (mE' = "none" \/ (mE' = "pending" /\ ~mI' /\ ~mF'))
                           /\ resMetaFollow' = (mE' = "pending" /\ (mI' \/ mF'))
                           /\ IF tE = "none"
                                 THEN /\ \E k \in Kinds:
                                           /\ tE' = "pending"
                                           /\ tK' = k
                                           /\ tI' = FALSE
                                           /\ tF' = FALSE
                                 ELSE /\ IF tE = "pending" /\ tI /\ ~tF
                                            THEN /\ tF' = TRUE
                                            ELSE /\ TRUE
                                                 /\ tF' = tF
                                      /\ UNCHANGED << tE, tK, tI >>
                           /\ IF FALSE
                                 THEN /\ timeline' = (IF tE' = "done" THEN "ready" ELSE "syncing")
                                      /\ tlCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                      /\ tlFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                      /\ UNCHANGED << resMsgCur, resMsgFollow >>
                                 ELSE /\ resMsgCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                      /\ resMsgFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                      /\ UNCHANGED << timeline, tlCur, 
                                                      tlFollow >>
                      ELSE /\ IF res = "ready"
                                 THEN /\ IF mE = "none"
                                            THEN /\ \E k \in Kinds:
                                                      /\ mE' = "pending"
                                                      /\ mK' = k
                                                      /\ mI' = FALSE
                                                      /\ mF' = FALSE
                                            ELSE /\ IF mE = "pending" /\ mI /\ ~mF
                                                       THEN /\ mF' = TRUE
                                                       ELSE /\ TRUE
                                                            /\ mF' = mF
                                                 /\ UNCHANGED << mE, mK, mI >>
                                      /\ resMetaDone' = (mE' = "done")
                                      /\ resMetaCur' = (mE' = "none" \/ (mE' = "pending" /\ ~mI' /\ ~mF'))
                                      /\ resMetaFollow' = (mE' = "pending" /\ (mI' \/ mF'))
                                      /\ IF tE = "none"
                                            THEN /\ \E k \in Kinds:
                                                      /\ tE' = "pending"
                                                      /\ tK' = k
                                                      /\ tI' = FALSE
                                                      /\ tF' = FALSE
                                            ELSE /\ IF tE = "pending" /\ tI /\ ~tF
                                                       THEN /\ tF' = TRUE
                                                       ELSE /\ TRUE
                                                            /\ tF' = tF
                                                 /\ UNCHANGED << tE, tK, tI >>
                                      /\ IF FALSE
                                            THEN /\ timeline' = (IF tE' = "done" THEN "ready" ELSE "syncing")
                                                 /\ tlCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                                 /\ tlFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                                 /\ UNCHANGED << resMsgCur, 
                                                                 resMsgFollow >>
                                            ELSE /\ resMsgCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                                                 /\ resMsgFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                                                 /\ UNCHANGED << timeline, 
                                                                 tlCur, 
                                                                 tlFollow >>
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << resMetaCur, 
                                                      resMetaFollow, 
                                                      resMetaDone, resMsgCur, 
                                                      resMsgFollow, mE, mK, mI, 
                                                      mF, tE, tK, tI, tF, 
                                                      timeline, tlCur, 
                                                      tlFollow >>
                           /\ res' = res
                /\ UNCHANGED dropped
             \/ /\ conn = "connected" /\ Outage
                /\ conn' = "reconnecting"
                /\ dropped' = TRUE
                /\ IF mE = "done"
                      THEN /\ mE' = "none"
                           /\ mI' = mI
                      ELSE /\ IF mE = "pending" /\ ~mF
                                 THEN /\ mI' = TRUE
                                 ELSE /\ TRUE
                                      /\ mI' = mI
                           /\ mE' = mE
                /\ IF tE = "done"
                      THEN /\ tE' = "none"
                           /\ tI' = tI
                      ELSE /\ IF tE = "pending" /\ ~tF
                                 THEN /\ tI' = TRUE
                                 ELSE /\ TRUE
                                      /\ tI' = tI
                           /\ tE' = tE
                /\ UNCHANGED <<res, resMetaCur, resMetaFollow, resMetaDone, resMsgCur, resMsgFollow, mK, mF, tK, tF, timeline, tlCur, tlFollow>>
          /\ pc' = [pc EXCEPT !["connection"] = "Stream"]
          /\ UNCHANGED << rcDown, ocDown, outages, list, mounted, health, 
                          opened, resMsgFailed, resMetaRetry, served, 
                          metaCached, loaded, mOk, mQueued, tOk, tQueued, tHit >>

Connection == Stream

Probe == /\ pc["health"] = "Probe"
         /\ ~Flush /\ mounted
         /\ \/ /\ Outage /\ health = "ok"
               /\ health' = "unreachable"
            \/ /\ ~Outage /\ health = "unreachable"
               /\ health' = "ok"
         /\ pc' = [pc EXCEPT !["health"] = "Probe"]
         /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, conn, 
                         opened, res, resMetaCur, resMetaFollow, resMetaDone, 
                         resMsgCur, resMsgFollow, resMsgFailed, resMetaRetry, 
                         served, metaCached, loaded, mE, mK, mI, mF, tE, tK, 
                         tI, tF, timeline, tlCur, tlFollow, mOk, mQueued, tOk, 
                         tQueued, tHit >>

Health == Probe

SettleMeta == /\ pc["meta"] = "SettleMeta"
              /\ ~Flush /\ mounted /\ mE = "pending" /\ (mK = "stall" => ~(res = "resolving" /\ resMetaCur))
              /\ mOk' = (mK = "ok")
              /\ mQueued' = mF
              /\ metaCached' = (metaCached \/ mOk')
              /\ res' = (IF mOk' /\ res # "idle" THEN "ready"
                         ELSE IF resMetaCur /\ res \in {"resolving", "unreachable"}
                              THEN (IF MetaUnreachable /\ mK # "error" THEN "unreachable" ELSE "error")
                         ELSE res)
              /\ resMetaRetry' = (MetaUnreachable /\ ~mOk' /\ mK # "error" /\ resMetaCur /\ res' \in {"resolving", "unreachable"})
              /\ served' = (served \/ mK = "error")
              /\ IF mQueued'
                    THEN /\ \E k \in Kinds:
                              /\ mK' = k
                              /\ mI' = FALSE
                              /\ mF' = FALSE
                         /\ mE' = mE
                    ELSE /\ mE' = (IF mOk' /\ ~mI THEN "done" ELSE "none")
                         /\ UNCHANGED << mK, mI, mF >>
              /\ resMetaCur' = (mQueued' /\ resMetaFollow)
              /\ resMetaFollow' = FALSE
              /\ pc' = [pc EXCEPT !["meta"] = "SettleMeta"]
              /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, 
                              conn, health, opened, resMetaDone, resMsgCur, 
                              resMsgFollow, resMsgFailed, loaded, tE, tK, tI, 
                              tF, timeline, tlCur, tlFollow, tOk, tQueued, 
                              tHit >>

MetaServer == SettleMeta

SettleMsgs == /\ pc["msgs"] = "SettleMsgs"
              /\ ~Flush /\ mounted /\ tE = "pending" /\ (tK = "stall" => ~(timeline = "syncing" /\ tlCur))
              /\ tOk' = (tK = "ok")
              /\ tQueued' = tF
              /\ tHit' = (timeline = "syncing" /\ tlCur)
              /\ loaded' = (loaded \/ tOk')
              /\ timeline' = IF tHit' THEN (IF tOk' \/ (TransportAsTimeout /\ tK # "error") THEN "ready" ELSE "failed") ELSE timeline
              /\ resMsgFailed' = (IF resMsgCur THEN ~tOk' /\ tK # "error" ELSE resMsgFailed)
              /\ served' = (served \/ tK = "error")
              /\ IF tQueued'
                    THEN /\ \E k \in Kinds:
                              /\ tK' = k
                              /\ tI' = FALSE
                              /\ tF' = FALSE
                         /\ tE' = tE
                    ELSE /\ tE' = (IF tOk' /\ ~tI THEN "done" ELSE "none")
                         /\ UNCHANGED << tK, tI, tF >>
              /\ tlCur' = (tQueued' /\ tlFollow)
              /\ tlFollow' = FALSE
              /\ resMsgCur' = (tQueued' /\ resMsgFollow)
              /\ resMsgFollow' = FALSE
              /\ pc' = [pc EXCEPT !["msgs"] = "SettleMsgs"]
              /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, 
                              conn, health, opened, res, resMetaCur, 
                              resMetaFollow, resMetaDone, resMetaRetry, 
                              metaCached, mE, mK, mI, mF, mOk, mQueued >>

MsgServer == SettleMsgs

Start == /\ pc["timeline"] = "Start"
         /\ Flush
         /\ IF tE = "none"
               THEN /\ \E k \in Kinds:
                         /\ tE' = "pending"
                         /\ tK' = k
                         /\ tI' = FALSE
                         /\ tF' = FALSE
               ELSE /\ IF tE = "pending" /\ tI /\ ~tF
                          THEN /\ tF' = TRUE
                          ELSE /\ TRUE
                               /\ tF' = tF
                    /\ UNCHANGED << tE, tK, tI >>
         /\ IF TRUE
               THEN /\ timeline' = (IF tE' = "done" THEN "ready" ELSE "syncing")
                    /\ tlCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                    /\ tlFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                    /\ UNCHANGED << resMsgCur, resMsgFollow >>
               ELSE /\ resMsgCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                    /\ resMsgFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                    /\ UNCHANGED << timeline, tlCur, tlFollow >>
         /\ pc' = [pc EXCEPT !["timeline"] = "Start"]
         /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, conn, 
                         health, opened, res, resMetaCur, resMetaFollow, 
                         resMetaDone, resMsgFailed, resMetaRetry, served, 
                         metaCached, loaded, mE, mK, mI, mF, mOk, mQueued, tOk, 
                         tQueued, tHit >>

TimelineEffect == Start

Cached == /\ pc["metadone"] = "Cached"
          /\ ~Flush /\ mounted /\ resMetaDone
          /\ resMetaDone' = FALSE
          /\ IF res \in {"resolving", "unreachable"}
                THEN /\ res' = "ready"
                ELSE /\ TRUE
                     /\ res' = res
          /\ pc' = [pc EXCEPT !["metadone"] = "Cached"]
          /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, 
                          conn, health, opened, resMetaCur, resMetaFollow, 
                          resMsgCur, resMsgFollow, resMsgFailed, resMetaRetry, 
                          served, metaCached, loaded, mE, mK, mI, mF, tE, tK, 
                          tI, tF, timeline, tlCur, tlFollow, mOk, mQueued, tOk, 
                          tQueued, tHit >>

MetaDone == Cached

ResolveTimeout == /\ pc["restimer"] = "ResolveTimeout"
                  /\ ~Flush /\ mounted /\ res = "resolving" /\ ~(resMetaCur /\ mE = "pending" /\ Fast(mK))
                  /\ res' = "unreachable"
                  /\ pc' = [pc EXCEPT !["restimer"] = "ResolveTimeout"]
                  /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, 
                                  mounted, conn, health, opened, resMetaCur, 
                                  resMetaFollow, resMetaDone, resMsgCur, 
                                  resMsgFollow, resMsgFailed, resMetaRetry, 
                                  served, metaCached, loaded, mE, mK, mI, mF, 
                                  tE, tK, tI, tF, timeline, tlCur, tlFollow, 
                                  mOk, mQueued, tOk, tQueued, tHit >>

ResolutionTimer == ResolveTimeout

SyncTimeout == /\ pc["tltimer"] = "SyncTimeout"
               /\ ~Flush /\ mounted /\ timeline = "syncing" /\ ~(tlCur /\ tE = "pending" /\ Fast(tK))
               /\ timeline' = "ready"
               /\ tlCur' = FALSE
               /\ tlFollow' = FALSE
               /\ pc' = [pc EXCEPT !["tltimer"] = "SyncTimeout"]
               /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, 
                               conn, health, opened, res, resMetaCur, 
                               resMetaFollow, resMetaDone, resMsgCur, 
                               resMsgFollow, resMsgFailed, resMetaRetry, 
                               served, metaCached, loaded, mE, mK, mI, mF, tE, 
                               tK, tI, tF, mOk, mQueued, tOk, tQueued, tHit >>

TimelineTimer == SyncTimeout

Again == /\ pc["retry"] = "Again"
         /\ ~Flush /\ mounted /\ RetryTranscript /\ resMsgFailed /\ res # "idle"
         /\ resMsgFailed' = FALSE
         /\ IF tE = "none"
               THEN /\ \E k \in Kinds:
                         /\ tE' = "pending"
                         /\ tK' = k
                         /\ tI' = FALSE
                         /\ tF' = FALSE
               ELSE /\ IF tE = "pending" /\ tI /\ ~tF
                          THEN /\ tF' = TRUE
                          ELSE /\ TRUE
                               /\ tF' = tF
                    /\ UNCHANGED << tE, tK, tI >>
         /\ IF FALSE
               THEN /\ timeline' = (IF tE' = "done" THEN "ready" ELSE "syncing")
                    /\ tlCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                    /\ tlFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                    /\ UNCHANGED << resMsgCur, resMsgFollow >>
               ELSE /\ resMsgCur' = (tE' = "none" \/ (tE' = "pending" /\ ~tI' /\ ~tF'))
                    /\ resMsgFollow' = (tE' = "pending" /\ (tI' \/ tF'))
                    /\ UNCHANGED << timeline, tlCur, tlFollow >>
         /\ pc' = [pc EXCEPT !["retry"] = "Again"]
         /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, conn, 
                         health, opened, res, resMetaCur, resMetaFollow, 
                         resMetaDone, resMetaRetry, served, metaCached, loaded, 
                         mE, mK, mI, mF, mOk, mQueued, tOk, tQueued, tHit >>

Retry == Again

MetaAgain == /\ pc["metaretry"] = "MetaAgain"
             /\ ~Flush /\ mounted /\ resMetaRetry /\ res \in {"resolving", "unreachable"}
             /\ resMetaRetry' = FALSE
             /\ IF mE = "none"
                   THEN /\ \E k \in Kinds:
                             /\ mE' = "pending"
                             /\ mK' = k
                             /\ mI' = FALSE
                             /\ mF' = FALSE
                   ELSE /\ IF mE = "pending" /\ mI /\ ~mF
                              THEN /\ mF' = TRUE
                              ELSE /\ TRUE
                                   /\ mF' = mF
                        /\ UNCHANGED << mE, mK, mI >>
             /\ resMetaDone' = (mE' = "done")
             /\ resMetaCur' = (mE' = "none" \/ (mE' = "pending" /\ ~mI' /\ ~mF'))
             /\ resMetaFollow' = (mE' = "pending" /\ (mI' \/ mF'))
             /\ pc' = [pc EXCEPT !["metaretry"] = "MetaAgain"]
             /\ UNCHANGED << rcDown, ocDown, outages, dropped, list, mounted, 
                             conn, health, opened, res, resMsgCur, 
                             resMsgFollow, resMsgFailed, served, metaCached, 
                             loaded, tE, tK, tI, tF, timeline, tlCur, tlFollow, 
                             mOk, mQueued, tOk, tQueued, tHit >>

MetaRetry == MetaAgain

Next == Person \/ Network \/ RocketClaw \/ Connection \/ Health
           \/ MetaServer \/ MsgServer \/ TimelineEffect \/ MetaDone
           \/ ResolutionTimer \/ TimelineTimer \/ Retry \/ MetaRetry

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Person)
        /\ WF_vars(Network)
        /\ WF_vars(RocketClaw)
        /\ WF_vars(Connection)
        /\ WF_vars(Health)
        /\ WF_vars(MetaServer)
        /\ WF_vars(MsgServer)
        /\ WF_vars(TimelineEffect)
        /\ WF_vars(MetaDone)
        /\ WF_vars(ResolutionTimer)
        /\ WF_vars(TimelineTimer)
        /\ WF_vars(Retry)
        /\ WF_vars(MetaRetry)

\* END TRANSLATION
=============================================================================
