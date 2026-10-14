"""Turn a TUI snapshot (ANSI truecolor text) into an HTML terminal window.

    PROSVET_SNAPSHOT_IDLE=1 PROSVET_SNAPSHOT_SIZE=118x26 PROSVET_SNAPSHOT=/tmp/snap \
        go test -run Snapshot ./internal/tui
    python3 tools/screenshot/ansi2html.py /tmp/snap/Обзор.ans /tmp/snap/shot.html
    firefox --headless --screenshot docs/screenshot.png --window-size=1086,558 file:///tmp/snap/shot.html
"""
import html
import re
import sys

src, out = sys.argv[1], sys.argv[2]
data = open(src, encoding="utf-8").read()
fg = bg = None
bold = False
res = []
for tok in re.split(r'(\x1b\[[0-9;]*m)', data):
    if tok.startswith('\x1b['):
        codes = [int(c) if c else 0 for c in tok[2:-1].split(';')] or [0]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0:
                fg = bg = None
                bold = False
            elif c == 1:
                bold = True
            elif c == 22:
                bold = False
            elif c == 39:
                fg = None
            elif c == 49:
                bg = None
            elif c in (38, 48) and i + 4 < len(codes) and codes[i + 1] == 2:
                col = '#%02x%02x%02x' % tuple(codes[i + 2:i + 5])
                if c == 38:
                    fg = col
                else:
                    bg = col
                i += 4
            i += 1
        continue
    if not tok:
        continue
    st = []
    if fg:
        st.append('color:' + fg)
    if bg:
        st.append('background:' + bg)
    if bold:
        st.append('font-weight:700')
    t = html.escape(tok)
    res.append('<span style="%s">%s</span>' % (';'.join(st), t) if st else t)

page = '''<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;background:#0d0d0d}
.win{margin:28px;border-radius:12px;overflow:hidden;border:1px solid #2a2a2a;box-shadow:0 18px 50px rgba(0,0,0,.5);display:inline-block;background:#111}
.bar{height:34px;background:#181818;display:flex;align-items:center;gap:8px;padding:0 14px}
.bar i{width:11px;height:11px;border-radius:50%;background:#333;display:block}
.bar span{color:#8e8e8e;font:13px "DejaVu Sans Mono",monospace;margin-left:10px}
pre{margin:0;padding:14px 16px 16px;color:#e6e6e6;font:14px/1.2 "DejaVu Sans Mono","JetBrains Mono",monospace}
</style><div class="win"><div class="bar"><i></i><i></i><i></i><span>zapret</span></div><pre>'''
open(out, 'w', encoding='utf-8').write(page + ''.join(res) + '</pre></div>')
