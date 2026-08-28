"""Count each document's Tests bullets against the tests its packages have.

Not a correctness check. It finds the document whose test list is far larger
than the tests written for it, which is where an unwritten spec test hides.
"""
import os, re, subprocess, collections

DOCS = "../docs/refactor"
AREA_DIRS = {
    "foundation": ("engine/kit/", "engine/infra/", "engine/store/",
                   "engine/service/acl/"),
    "core":       ("engine/service/core/",),
    "auth":       ("engine/service/auth/",),
    "oidc":       ("engine/service/oidc/",),
    "upload":     ("engine/service/upload/",),
    "search":     ("engine/service/search/",),
    "preview":    ("engine/service/preview/",),
    "watch":      ("engine/service/watch/",),
    "settings":   ("engine/service/settings/", "engine/http/emergency/"),
    "smb":        ("engine/service/smb/",),
}

def bullets(text):
    """Top-level bullets under a '## Tests' heading."""
    n = 0
    for sec in re.findall(r"^## Tests\n(.*?)(?=^## |\Z)", text, re.S | re.M):
        n += len(re.findall(r"^[-*] ", sec, re.M))
        n += len(re.findall(r"^\d+\. ", sec, re.M))
    return n

doc_counts = collections.Counter()
for area in AREA_DIRS:
    d = os.path.join(DOCS, area)
    if not os.path.isdir(d):
        continue
    for fn in sorted(os.listdir(d)):
        if fn.endswith(".md"):
            doc_counts[area] += bullets(open(os.path.join(d, fn)).read())

tree_counts = collections.Counter()
for area, dirs in AREA_DIRS.items():
    for d in dirs:
        out = subprocess.run(["grep", "-rh", "--include=*_test.go", "-E",
                              r"^func (Test|Fuzz)", d], capture_output=True, text=True)
        tree_counts[area] += len([l for l in out.stdout.split("\n") if l.strip()])

print(f"{'area':12s} {'spec bullets':>12s} {'tests written':>14s}  ratio")
for area in sorted(AREA_DIRS):
    b, t = doc_counts[area], tree_counts[area]
    r = t / b if b else 0
    flag = "  <-- thin" if b and r < 1.0 else ""
    print(f"{area:12s} {b:12d} {t:14d}  {r:5.2f}{flag}")
print(f"{'TOTAL':12s} {sum(doc_counts.values()):12d} {sum(tree_counts.values()):14d}")
