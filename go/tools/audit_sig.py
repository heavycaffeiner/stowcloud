"""Compare each refactor document's declared func signatures against the engine.

Reports arity drift: a function the document declares with N parameters that
the tree implements with a different number. Names are matched, then arity.
"""
import re, os, subprocess, collections

DOCS = "../docs/refactor"
AREAS = ["foundation", "core", "auth", "oidc", "upload", "search", "preview",
         "watch", "settings", "smb"]

blockre = re.compile(r"^```go\n(.*?)^```", re.S | re.M)

# A declared func line, possibly wrapped over several lines. Joined first.
def declared_funcs(block):
    # Join continuation lines: a signature broken across lines always leaves
    # an unbalanced paren.
    joined, buf, depth = [], "", 0
    for line in block.split("\n"):
        # A trailing comment is prose, not a result type. Left in, "Destroy()
        # // zeroes the buffer" reads as a one-result function.
        s = re.sub(r"//.*$", "", line).strip()
        if not buf and not s.startswith("func"):
            continue
        buf = (buf + " " + s).strip() if buf else s
        depth += s.count("(") - s.count(")")
        if depth <= 0:
            joined.append(buf)
            buf, depth = "", 0
    return joined

def split_top(s):
    """Split a parameter list on top-level commas."""
    out, depth, cur = [], 0, ""
    for ch in s:
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "," and depth == 0:
            out.append(cur.strip()); cur = ""
        else:
            cur += ch
    if cur.strip():
        out.append(cur.strip())
    return out

def arity(params):
    """Count parameters, expanding 'a, b int' groups the way Go does."""
    parts = split_top(params)
    if not parts:
        return 0
    n = 0
    for p in parts:
        # "a, b int" already split; each part is one param unless it is a bare
        # type in a group, which the group-expansion below handles.
        n += 1
    return n

sigre = re.compile(r"^func\s+(?:\(\s*\w*\s*\*?[\w\[\]]+\s*\)\s*)?([A-Z]\w*)\s*(?:\[[^\]]*\])?\((.*)$")

doc_sigs = collections.defaultdict(list)   # name -> [(params, results, doc)]
for area in AREAS:
    d = os.path.join(DOCS, area)
    if not os.path.isdir(d):
        continue
    for fn in sorted(os.listdir(d)):
        if not fn.endswith(".md"):
            continue
        text = open(os.path.join(d, fn)).read()
        for blk in blockre.findall(text):
            for line in declared_funcs(blk):
                m = sigre.match(line)
                if not m:
                    continue
                name, rest = m.group(1), m.group(2)
                # Find the matching close paren for the parameter list.
                depth, params, i = 1, "", 0
                while i < len(rest) and depth > 0:
                    ch = rest[i]
                    if ch == "(":
                        depth += 1
                    elif ch == ")":
                        depth -= 1
                        if depth == 0:
                            break
                    params += ch
                    i += 1
                results = rest[i+1:].strip().rstrip("{").strip()
                # Result arity.
                if not results:
                    rn = 0
                elif results.startswith("("):
                    rn = len(split_top(results[1:results.rfind(")")]))
                else:
                    rn = 1
                # "(...)" and "(..., total uint64)" are the documents' way of
                # writing "the usual context, id and user". An elided list
                # carries no arity to compare, so comparing one reports drift
                # that is not there.
                if params.strip().startswith("..."):
                    continue
                doc_sigs[name].append((arity(params), rn, area + "/" + fn, line))

# The engine's own arities.
out = subprocess.run(["go", "run", "./tools/sigscan", "engine/"],
                     capture_output=True, text=True)
tree = collections.defaultdict(list)
for line in out.stdout.strip().split("\n"):
    if not line:
        continue
    name, p, r, path = line.split("\t")
    tree[name].append((int(p), int(r), path))

# Which engine directories an area's documents are about. Without this, a
# document's Snapshot is compared against every Snapshot in the tree and any
# one of them matching hides the drift.
AREA_DIRS = {
    # acl-evaluator.md and search-contract.md are filed under foundation by
    # build order, but their packages land in the service tier.
    "foundation": ("engine/kit/", "engine/infra/", "engine/store/",
                   "engine/service/acl/", "engine/service/core/"),
    "core":       ("engine/service/core/", "engine/service/acl/", "engine/store/"),
    "auth":       ("engine/service/auth/",),
    "oidc":       ("engine/service/oidc/", "engine/service/auth/"),
    "upload":     ("engine/service/upload/", "engine/store/", "engine/infra/vfs/"),
    "search":     ("engine/service/search/",),
    "preview":    ("engine/service/preview/", "engine/infra/"),
    "watch":      ("engine/service/watch/",),
    "settings":   ("engine/service/settings/", "engine/http/emergency/"),
    "smb":        ("engine/service/smb/", "engine/service/core/"),
}

def in_area(path, area):
    return any(path.startswith(d) for d in AREA_DIRS[area])

drift, absent = [], []
for name, entries in sorted(doc_sigs.items()):
    if name not in tree:
        absent.append((name, entries[0][2]))
        continue
    for dp, dr, doc, line in entries:
        area = doc.split("/")[0]
        scoped = [e for e in tree[name] if in_area(e[2], area)]
        if not scoped:
            absent.append((name, doc))
            continue
        if any(dp == tp and dr == tr for tp, tr, _ in scoped):
            continue
        have = sorted({(p, r) for p, r, _ in scoped})
        drift.append((name, doc, dp, dr, have, line))

print(f"documented funcs: {len(doc_sigs)}   absent: {len(absent)}   arity drift: {len(drift)}")
print("\n--- absent ---")
for n, d in absent:
    print(f"  {n:30s} {d}")
print("\n--- arity drift ---")
for n, doc, dp, dr, have, line in drift:
    print(f"  {n:26s} {doc}")
    print(f"      doc {dp}p/{dr}r   tree {have}")
