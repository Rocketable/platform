import json, sys

MODEL = "gpt-6.1-sol"
STICKIES = ["off", "user", "global", "slack"]
RECVS = ["absent", "true", "false", "list"]
SUDO_HOST = "st-off-absent"
PLAIN_HOST = "at-off-absent"


def build():
    agents = {}

    def add(name, **a):
        a.setdefault("caller", False)
        a.setdefault("tools", False)
        a.setdefault("recv", "absent")
        a.setdefault("rule", "none")
        a.setdefault("entries", False)
        a.setdefault("copies", False)
        a.setdefault("builtin", "no")
        a.setdefault("sudo", False)
        agents[name] = dict(name=name, **a)

    for rule in ["none", "allow", "auto", "deny", "wild"]:
        for sticky in ["off", "slack"]:
            add(f"ac-{rule}-{sticky}", kind="advanced", sticky=sticky, rule=rule, caller=True, tools=True)
    for rule in ["none", "allow"]:
        add(f"an-{rule}-off", kind="advanced", sticky="off", rule=rule, caller=True, tools=False)
    for rule in ["none", "allow", "auto", "deny", "wild"]:
        add(f"sc-{rule}-off", kind="advanced", sudo=True, sticky="off", rule=rule, caller=True, tools=True)
    for rule in ["none", "deny"]:
        add(f"sc-{rule}-slack", kind="advanced", sudo=True, sticky="slack", rule=rule, caller=True, tools=True)
    add("sn-none-off", kind="advanced", sudo=True, sticky="off", rule="none", caller=True, tools=False)
    for e in [True, False]:
        for c in [True, False]:
            add(f"ec-{'y' if e else 'n'}{'y' if c else 'n'}-off", kind="easy", sticky="off", entries=e, copies=c, caller=True, tools=True)
        add(f"ec-{'y' if e else 'n'}y-slack", kind="easy", sticky="slack", entries=e, copies=True, caller=True, tools=True)
    add("ar-allow-off", kind="advanced", sticky="off", rule="allow", recv="false", caller=True, tools=True)
    add("er-yy-off", kind="easy", sticky="off", entries=True, copies=True, recv="false", caller=True, tools=True)
    add("en-yy-off", kind="easy", sticky="off", entries=True, copies=True, caller=True, tools=False)
    for s in STICKIES:
        for r in RECVS:
            add(f"at-{s}-{r}", kind="advanced", sticky=s, recv=r)
            add(f"et-{s}-{r}", kind="easy", sticky=s, recv=r, copies=True)
    for s in ["off", "global"]:
        for r in RECVS:
            add(f"st-{s}-{r}", kind="advanced", sudo=True, sticky=s, recv=r)
    add("main", kind="advanced", sudo=True, sticky="global", builtin="main", caller=True, tools=True)

    callers = sorted(n for n, a in agents.items() if a["caller"])
    listed = {c for i, c in enumerate(callers) if i % 2 == 0}
    for a in agents.values():
        a["list"] = sorted(listed) if a["recv"] == "list" else []
    return agents, callers


def agent_file(a, version, names):
    if a["builtin"] != "no":
        return None
    old = version == "old"
    lines = ["---", f"model: {MODEL}"]
    if a["kind"] != "easy":
        lines.insert(1, "description: Agent")
    if a["kind"] == "easy":
        lines += ["mode: easy", "space: Ops", "workingDirectory: work"]
        if a["sticky"] != "off":
            lines.append(f"sticky: {a['sticky']}")
        lines.append("rocketclaw: [sessions]" if a["tools"] else "rocketclaw: []")
        entries = []
        if a["copies"]:
            entries.append(MODEL)
        if a["entries"] and not old:
            entries += [f"{{agent: {n}}}" for n in names]
        lines.append("subagents: [" + ", ".join(entries) + "]")
        if old and a["recv"] != "absent":
            lines.append("delegatedFrom: " + ("[" + ", ".join(a["list"]) + "]" if a["recv"] == "list" else a["recv"]))
    else:
        if a["sudo"]:
            lines.append("sudo: yes")
        if a["sticky"] != "off":
            lines.append(f"sticky: {a['sticky']}")
        rc = []
        if a["tools"] and not a["sudo"]:
            rc.append("rocketclaw_sessions: allow")
        if not a["tools"] and a["sudo"]:
            rc.append("rocketclaw_sessions: deny")
        if old and a["recv"] != "absent":
            rc.append("rocketclaw_sessions_recipient: " + ("[" + ", ".join(a["list"]) + "]" if a["recv"] == "list" else a["recv"]))
        task = []
        if a["rule"] == "wild":
            task.append('"*": allow')
        elif a["rule"] != "none":
            task += [f"{n}: {a['rule']}" for n in names]
        if rc or task:
            lines.append("permission:")
        if rc:
            lines.append("  rocketclaw:")
            lines += ["    " + x for x in rc]
        if task:
            lines.append("  task:")
            lines += ["    " + x for x in task]
    lines += ["---", "Prompt", ""]
    return "\n".join(lines)


def sessions_for(agents, callers):
    sessions = []
    names = sorted(agents)

    def s(id, agent, kind, parent="", channel="", thread="", entry=True):
        sessions.append(dict(id=id, agent=agent, kind=kind, parent=parent, channel=channel, thread=thread, entry=entry))

    for t in names:
        s(f"web:{t}", t, "web")
    for t in names:
        s(f"web:{t}/own", t, "helper", parent=f"web:{t}")
        if t != SUDO_HOST:
            s(f"web:{SUDO_HOST}/h-{t}", t, "helper", parent=f"web:{SUDO_HOST}")
        if t != PLAIN_HOST:
            s(f"web:{PLAIN_HOST}/h-{t}", t, "helper", parent=f"web:{PLAIN_HOST}")
    for c in callers:
        for t in names:
            if t != c:
                s(f"web:{c}/h-{t}", t, "helper", parent=f"web:{c}")
    for i, c in enumerate(callers):
        s(f"run:{c}", c, "run", entry=False)
        if agents[c]["sticky"] == "slack":
            s(f"slack-channel:D{i}:{c}", c, "slack", channel=f"D{i}", entry=False)
            s(f"slack-thread:D{i}:1.1:{c}", c, "slack", channel=f"D{i}", thread="1.1")
    minute = 0
    for x in sessions:
        x["minute"] = minute if x["entry"] else -1
        if x["entry"]:
            minute += 1
    return sessions


WINDOW = 150


def turns_for(agents, callers, sessions, version):
    names = sorted(agents)
    entried = [x for x in sessions if x["entry"]]
    total = len(entried)
    turns = []
    for c in callers:
        calls = []

        def ex(cid, code):
            calls.append(dict(id=cid, name="execute", arguments=json.dumps({"code": "def main():\n    return " + code + "\n", "description": cid})))

        for t in names:
            args = {"description": "d", "prompt": "p"}
            args["agent" if version == "new" else "subagent_type"] = t
            calls.append(dict(id=f"start|{t}", name="task", arguments=json.dumps(args)))
        for x in sessions:
            if x["kind"] == "web" or (x["kind"] == "slack" and x["thread"]) or (x["kind"] == "helper" and x["id"].endswith("/own")):
                ex(f"write|{x['id']}", f'rocketclaw_sessions_write(session_id={json.dumps(x["id"])}, mode="enqueue", prompt="p", title="", agent="")')
        for x in entried:
            if x["kind"] == "helper" and not x["id"].startswith(f"web:{c}/") and x["parent"] not in (f"web:{SUDO_HOST}", f"web:{PLAIN_HOST}") and not x["id"].endswith("/own"):
                continue
            ex(f"read|{x['id']}", f'rocketclaw_sessions_get(conversation_id={json.dumps(x["id"])}, limit=1, before_entry_id=0)')
        for k in range(0, total, WINDOW):
            ex(f"list|{k}", f'rocketclaw_sessions_list(criteria="", since={json.dumps(stamp(k))}, until={json.dumps(stamp(k + WINDOW))}, limit=200, include_message_preview=False)')
        turns.append(dict(caller=c, self=f"run:{c}", calls=calls))
        if agents[c]["sticky"] == "slack":
            ch = next(x for x in sessions if x["agent"] == c and x["kind"] == "slack" and not x["thread"])
            th = next(x for x in sessions if x["agent"] == c and x["kind"] == "slack" and x["thread"])
            turns.append(dict(caller=c, self=ch["id"], calls=[dict(id=f"ownwrite|{th['id']}", name="execute", arguments=json.dumps({"code": "def main():\n    return " + f'rocketclaw_sessions_write(session_id={json.dumps(th["id"])}, mode="enqueue", prompt="p", title="", agent="")' + "\n", "description": "own"}))]))
    return turns


def stamp(minute):
    days, rem = divmod(minute, 1440)
    return f"2026-01-{1 + days:02d}T{rem // 60:02d}:{rem % 60:02d}:00Z"


def main():
    work = sys.argv[1]
    agents, callers = build()
    sessions = sessions_for(agents, callers)
    names = sorted(agents)
    for version in ["old", "new"]:
        files = {n: agent_file(a, version, names) for n, a in agents.items()}
        world = dict(agents={n: f for n, f in files.items() if f is not None}, sessions=sessions, turns=turns_for(agents, callers, sessions, version))
        with open(f"{work}/world-{version}.json", "w") as fh:
            json.dump(world, fh)
    with open(f"{work}/attrs.json", "w") as fh:
        json.dump(dict(agents=agents, sessions=sessions), fh)


main()
