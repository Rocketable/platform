import json, re, collections, sys

D, README = sys.argv[1], sys.argv[2]
A = json.load(open(f"{D}/attrs.json"))
agents, sessions = A["agents"], A["sessions"]
byid = {s["id"]: s for s in sessions}
WINDOW = 150
ALLOWED = {
    "start": lambda output: "write subagent: task subagents are not available here" in output,
    "write": lambda output: output.startswith('{"outcome"'),
    "read": lambda output: output.startswith("timestamp\trole\tcontent"),
}


def top_agent(s):
    return byid[s["parent"]]["agent"] if s["kind"] == "helper" else s["agent"]


def cases(v):
    res = {}
    for line in open(f"{D}/out-{v}.jsonl"):
        d = json.loads(line)
        c, output = d["caller"], d["output"]
        assert "SQLSTATE" not in output and "failed to connect" not in output, (v, d)
        op, arg = d["call"].split("|", 1)
        if op != "list":
            kind = {"ownwrite": "write"}.get(op, op)
            res[(c, kind, arg, op == "ownwrite")] = ALLOWED[kind](output)
            continue
        rows = output.strip().split("\n")[1:] if output.startswith("conversation_id\tturns") else []
        assert len(rows) < 200
        listed = {r.split("\t")[0] for r in rows}
        for s in sessions:
            if s["entry"] and int(arg) <= s["minute"] < int(arg) + WINDOW:
                res[(c, "list", s["id"], False)] = s["id"] in listed
    return res


def model_case(c, op, target_or_session, own_thread):
    ca = agents[c]
    if op == "start":
        t, helper, owner_top = target_or_session, False, target_or_session
    else:
        s = byid[target_or_session]
        t, helper, owner_top = s["agent"], s["kind"] == "helper", top_agent(s)
        if op in ("read", "list") and s["kind"] == "slack" and s["agent"] == c:
            own_thread = True
    ta = agents[t]
    self_ = t == c
    if not helper or owner_top == t:
        owner = "target"
    elif owner_top == c:
        owner = "caller"
    elif agents[owner_top]["sudo"]:
        owner = "otherSudo"
    else:
        owner = "otherPlain"
    if ta["recv"] == "list":
        recv = "listWith" if c in ta["list"] else "listWithout"
    else:
        recv = ta["recv"]
    if ca["builtin"] == "main":
        rule = "deny" if self_ else "none"
    elif ca["kind"] == "advanced":
        rule = {"wild": "wildcard"}.get(ca["rule"], ca["rule"])
    else:
        rule = "none"
    entry = ca["kind"] == "easy" and ca["builtin"] == "no" and ca["entries"]
    return dict(c=ca, t=ta, self=self_, rule=rule, entry=entry, tools=ca["tools"], op=op,
                ses=dict(helper=helper, owner=owner, ownThread=own_thread), recv=recv, owner_top=owner_top)


def owner_sudo(x):
    o = x["ses"]["owner"]
    return {"target": x["t"]["sudo"], "caller": x["c"]["sudo"], "otherSudo": True}.get(o, False)


def owned_by_caller(x):
    return x["ses"]["owner"] == "caller" or (x["ses"]["owner"] == "target" and x["self"])


def sudo_owned(x):
    return owner_sudo(x) and not owned_by_caller(x)


def named(x):
    return x["entry"] if x["c"]["kind"] == "easy" else x["rule"] in ("allow", "auto", "wildcard")


def task_gate(x):
    if x["c"]["sudo"] and x["rule"] == "none":
        return "allow"
    if x["rule"] == "none":
        return "deny"
    if x["rule"] == "wildcard":
        return "allow"
    return x["rule"]


def old_accepts(x):
    if x["recv"] in ("true", "listWith"):
        return True
    if x["recv"] == "absent":
        return x["t"]["kind"] == "easy" or not x["t"]["sudo"]
    return False


def old_allowed(x):
    op = x["op"]
    if op == "start":
        return x["c"]["kind"] == "advanced" and x["t"]["kind"] == "advanced" and task_gate(x) != "deny" and (x["t"]["sticky"] == "off" or x["self"])
    if op == "write":
        return x["tools"] and not x["ses"]["helper"] and (x["ses"]["ownThread"] or old_accepts(x))
    return x["tools"]


def new_gate(x):
    if x["c"]["kind"] == "easy":
        return "auto" if x["entry"] else "deny"
    return task_gate(x)


def hermetic(x):
    return sudo_owned(x) and not (x["c"]["sudo"] and x["op"] in ("read", "list"))


def sudo_denies(x):
    return x["c"]["sudo"] and not x["self"] and x["rule"] == "deny"


def reaches(x):
    return (not sudo_denies(x) if x["c"]["sudo"] else new_gate(x) != "deny") and not hermetic(x)


def new_allowed(x):
    op = x["op"]
    if op == "start":
        return new_gate(x) != "deny" and (x["t"]["sticky"] == "off" or x["self"]) and not hermetic(x)
    if op == "write":
        return x["tools"] and not x["ses"]["helper"] and reaches(x)
    return x["tools"] and reaches(x)


CATS = ["sudo-hermetic", "sudo-denial-refused", "unnamed-refused", "self-needs-naming", "slack-own-thread-needs-naming",
        "sudo-acts-on-own-sessions", "receiver-lists-gone", "task-reaches-easy-agents",
        "easy-tasks-listed-agents"]


def in_category(k, x, old, new):
    gained, lost = (not old) and new, old and not new
    op = x["op"]
    so = ("read", "write", "list")
    if k == "task-reaches-easy-agents":
        return gained and op == "start" and x["c"]["kind"] == "advanced" and x["t"]["kind"] == "easy"
    if k == "easy-tasks-listed-agents":
        return gained and op == "start" and x["c"]["kind"] == "easy" and x["entry"]
    if k == "sudo-hermetic":
        return lost and sudo_owned(x) and not (x["c"]["sudo"] and op in ("read", "list"))
    if k == "sudo-acts-on-own-sessions":
        return gained and x["c"]["sudo"] and owned_by_caller(x)
    if k == "sudo-denial-refused":
        return lost and op in so and sudo_denies(x)
    if k == "unnamed-refused":
        return lost and op in so and not x["c"]["sudo"] and not x["self"] and not named(x)
    if k == "self-needs-naming":
        return lost and op in so and not x["c"]["sudo"] and x["self"] and not named(x) and not (op == "write" and x["ses"]["ownThread"])
    if k == "slack-own-thread-needs-naming":
        return lost and op == "write" and x["ses"]["ownThread"] and not named(x)
    if k == "receiver-lists-gone":
        return gained and op == "write" and not old_accepts(x) and x["t"]["builtin"] == "no"


def agent_class(a):
    if a["builtin"] != "no":
        return a["builtin"]
    return "sudo advanced" if a["sudo"] else a["kind"]


def signature(x, new):
    tc = "self" if x["self"] else agent_class(x["t"])
    if x["op"] == "start" and x["t"]["sticky"] != "off":
        tc += " (sticky)"
    s = x["ses"]
    if x["op"] == "start":
        sc = "-"
    elif s["ownThread"]:
        sc = "own Slack thread"
    elif not s["helper"]:
        sc = "top-level"
    else:
        sc = {"target": "helper in its own chat", "caller": "helper in caller's chat", "otherSudo": "helper in a sudo agent's chat"}.get(s["owner"], "helper in a non-sudo agent's chat")
    nm = "sudo, denies target" if sudo_denies(x) else "sudo" if x["c"]["sudo"] else ("named" if named(x) else "unnamed")
    return f"{x['op']} | {agent_class(x['c'])} | {nm} | {tc} | {sc} | {'now allowed' if new else 'now refused'}"


def readme_rows():
    rows, cat = set(), None
    for line in open(README):
        m = re.match(r"#### `([^`]+)`", line)
        if m:
            cat = m.group(1)
        elif cat and line.startswith("| ") and not line.startswith("| Operation") and not line.startswith("|---"):
            cells = [c.strip() for c in line.strip().strip("|").split("|")]
            rows.add((cat, " | ".join(cells)))
    return rows


def main():
    old, new = cases("old"), cases("new")
    assert old.keys() == new.keys(), (len(old), len(new))
    rows = readme_rows()
    problems = collections.defaultdict(list)
    differences = 0
    for key in sorted(old):
        c, op, arg, own = key
        x = model_case(c, op, arg, own)
        co, cn = old[key], new[key]
        if co != old_allowed(x):
            problems["old code differs from OldAllowed"].append(key)
        if cn != new_allowed(x):
            problems["new code differs from NewAllowed"].append(key)
        if co != cn:
            differences += 1
            cats = [k for k in CATS if in_category(k, x, co, cn)]
            if not cats:
                problems["difference in no agreed category"].append(key)
            for k in cats:
                if (k, signature(x, cn)) not in rows:
                    problems["difference missing from the README list"].append((k, signature(x, cn)))
        if cn:
            if sudo_owned(x) and not (x["c"]["sudo"] and op in ("read", "list")):
                problems["SudoHermetic violated"].append(key)
            if not x["c"]["sudo"] and not named(x):
                problems["NamedOnly violated"].append(key)
            if x["self"] and not x["c"]["sudo"] and not named(x):
                problems["SelfOnlyWhenNamed violated"].append(key)
            if op == "start" and not (x["t"]["sticky"] == "off" or x["self"]):
                problems["StickyNotTasked violated"].append(key)
            if sudo_denies(x):
                problems["SudoDenialHolds violated"].append(key)
    print(f"{len(old)} cases, {differences} where the old and new code differ")
    for name, found in problems.items():
        print(f"{name}: {len(found)}, first {found[0]}")
    sys.exit(1 if problems else 0)


main()
