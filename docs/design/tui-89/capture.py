#!/usr/bin/env python3
"""Review-only PTY capture. Requires pexpect, pyte and Pillow; no app dependency."""
import hashlib
import html
import json
import os
import re
from pathlib import Path
import sys
import time

import pexpect
import pyte
from PIL import Image, ImageDraw, ImageFont

FIXTURES = Path(__file__).resolve().parent/'fixtures'
SEARCH = '--search' in sys.argv
SUPPORTING = '--supporting' in sys.argv
ACCEPTANCE = '--acceptance' in sys.argv
OUT = Path(__file__).resolve().parent
if ACCEPTANCE:
    OUT = OUT.parent/'tui-93'
    OUT.mkdir(exist_ok=True)
elif SEARCH:
    OUT = OUT.parent/'tui-91'
    OUT.mkdir(exist_ok=True)
elif SUPPORTING:
    OUT = OUT.parent/'tui-92'
    OUT.mkdir(exist_ok=True)
BINARY = sys.argv[1] if len(sys.argv) > 1 else '/tmp/agentsession-89-capture'
FONT = ImageFont.truetype('/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf', 14)
CELL, ROW = 9, 19


# pyte lacks CSI S/T, used by Bubble Tea's normal xterm scrolling renderer.
# These apply the existing index/reverse-index operations inside the margins.
class Screen(pyte.Screen):
    def scroll_up(self, count=1, **_):
        saved = self.cursor.y
        self.cursor.y = self.margins.bottom if self.margins else self.lines-1
        for _ in range(max(1, count)):
            self.index()
        self.cursor.y = saved

    def scroll_down(self, count=1, **_):
        saved = self.cursor.y
        self.cursor.y = self.margins.top if self.margins else 0
        for _ in range(max(1, count)):
            self.reverse_index()
        self.cursor.y = saved


class Stream(pyte.Stream):
    csi = dict(pyte.Stream.csi, S='scroll_up', T='scroll_down')
    events = pyte.Stream.events | {'scroll_up', 'scroll_down'}


def capture(theme):
    dark = theme != 'light'
    bg, fg = ('141b23', 'dce4ec') if dark else ('fafaf7', '25324a')
    if theme == 'mono':
        bg, fg = 'ffffff', '111111'
    env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor', NO_COLOR='1' if theme == 'mono' else '0')
    child = pexpect.spawn(BINARY, env=env, dimensions=(32, 120), encoding='utf-8', echo=False)
    screen = Screen(120, 32)
    stream = Stream(screen)
    started = time.monotonic()
    recording = [{'version': 2, 'width': 120, 'height': 32, 'title': 'AgentSession ' + ('#93 acceptance ' if ACCEPTANCE else '#92 supporting ' if SUPPORTING else '#91 search ' if SEARCH else '#89 ') + theme}]
    pending = ''

    def send(text):
        recording.append([round(time.monotonic()-started, 4), 'i', text])
        child.send(text)

    def drain(seconds=0.5):
        nonlocal pending
        until = time.monotonic()+seconds
        while time.monotonic() < until:
            try:
                chunk = child.read_nonblocking(65536, timeout=0.08)
            except pexpect.TIMEOUT:
                continue
            except pexpect.EOF:
                raise RuntimeError('TUI exited during capture: ' + child.before)
            recording.append([round(time.monotonic()-started, 4), 'o', chunk])
            stream.feed(chunk)
            pending += chunk
            if '\x1b]11;?' in pending:
                value = '1414/1b1b/2323' if dark else 'fafa/fafa/f7f7'
                child.send('\x1b]11;rgb:'+value+'\x1b\\')
                pending = ''
            pending = pending[-32:]

    def frame(name):
        text = '\n'.join(line.rstrip() for line in screen.display).rstrip('\n')+'\n'
        (OUT/(name+'-'+theme+'.txt')).write_text(text)
        image = Image.new('RGB', (screen.columns*CELL+24, screen.lines*ROW+24), '#'+bg)
        draw = ImageDraw.Draw(image)
        for y in range(screen.lines):
            for x in range(screen.columns):
                char = screen.buffer[y][x]
                char_fg = fg if char.fg == 'default' else char.fg
                char_bg = bg if char.bg == 'default' else char.bg
                palette = {'black':'000000','red':'aa0000','green':'00aa00','brown':'aa5500','blue':'0000aa','magenta':'aa00aa','cyan':'00aaaa','white':'aaaaaa','brightblack':'555555','brightwhite':'ffffff'}
                char_fg, char_bg = palette.get(char_fg,char_fg), palette.get(char_bg,char_bg)
                if char.reverse:
                    char_fg, char_bg = char_bg, char_fg
                at = (12+x*CELL, 12+y*ROW)
                draw.rectangle([at, (at[0]+CELL, at[1]+ROW)], fill='#'+char_bg)
                draw.text(at, char.data, font=FONT, fill='#'+char_fg, stroke_width=0)
        image.save(OUT/(name+'-'+theme+'.png'))

    def resize(cols, rows):
        screen.resize(rows, cols)
        child.setwinsize(rows, cols)
        recording.append([round(time.monotonic()-started,4), 'r', f'{cols}x{rows}'])
        drain()

    drain(1.5)
    assert 'PREVIEW' in '\n'.join(screen.display), '\n'.join(screen.display)
    if ACCEPTANCE:
        def matrix(name):
            for cols, rows in ((120,32), (80,24), (54,18), (40,12)):
                resize(cols, rows)
                frame(f'{name}-{cols}x{rows}')
            resize(80,24)

        def contains(text):
            assert text in '\n'.join(screen.display), '\n'.join(screen.display)

        matrix('sessions')
        send('\r'); drain()
        matrix('timeline')
        send(']'); drain()
        send('\x1b[6~'); drain()
        contains('10. Recorded evidence')
        frame('reading-80x24')
        send('?'); drain()
        matrix('help')
        send('\x1b'); drain()
        contains('10. Recorded evidence')
        send(']]\r'); drain()
        contains('RECORDED FAILURE')
        matrix('expanded')
        send('d'); drain()
        matrix('details')
        send('w' + '\x1b[C'*16); drain()
        contains('timeout=3000ms')
        frame('unwrapped-80x24')
        send('\x1b'); drain()
        contains('RECORDED FAILURE')
        send('/'); drain()
        send('\x1b[200~go tst\x1b[201~'+'\x1b[D'*2+'e'); drain()
        contains('go te█st')
        frame('editing-80x24')
        send('\x05\x1b[200~ q/?[]\x1b[201~'); drain()
        contains('q/?[]█')
        frame('paste-80x24')
        send('\x7f'*6+'\r'); drain()
        contains('Result 1/1')
        matrix('search')
        send('\r'); drain()
        contains('Timeline')
        frame('search-evidence-80x24')
        send('\x1b'); drain()
        contains('Query: go test')
        send('\x1b'); drain()
        contains('RECORDED FAILURE')
        frame('search-return-80x24')
        send('f'); drain()
        matrix('assessments')
        send(']'); drain()
        frame('supporting-reference-80x24')
        send('\r'); drain()
        contains('Timeline')
        frame('supporting-timeline-80x24')
        send('\x1b'); drain()
        contains('Assessments')
        send('\x1b'); drain()
        contains('RECORDED FAILURE')
        send('g'+']'*6+'d'); drain()
        contains('Press u')
        matrix('unknown-prompt')
        send('u'); drain()
        send('G'); drain()
        contains('raw stays hidden')
        matrix('unknown-raw')
        send('\x1b'); drain()
        send('x'); drain()
        matrix('projections')
        send('g'+'b'); drain(1)
        send('v'); drain()
        contains('attempts 2')
        frame('projection-rebuild-80x24')
        send('\x1b'); drain()
        contains('Timeline')
        send('g'+'r'); drain()
        contains('Fix request timeout')
        frame('refreshed-80x24')
        resize(32,10)
        contains('Resize to at least 40×12')
        frame('resize-32x10')
        resize(80,24)
        contains('Fix request timeout')
        send('\x1b'); drain()
        contains('Sessions')
        frame('returned-80x24')
        send('i'); drain()
        contains('Indexing completed successfully')
        matrix('indexing')
        send('\x1b'); drain()
        contains('Sessions')
        send('q')
    elif SUPPORTING:
        send('i'); drain()
        assert 'Indexing completed successfully' in '\n'.join(screen.display)
        frame('indexing-120x32')
        for cols, rows in ((80,24), (54,18), (40,12)):
            resize(cols, rows)
            frame(f'indexing-{cols}x{rows}')
        resize(80,24)
        send('?'); drain()
        frame('help-80x24')
        send('G'); drain()
        assert 'rebuild all' in '\n'.join(screen.display)
        frame('help-end-80x24')
        send('\x1b'); drain()
        send('\x1b'); drain()
        send('\r'); drain()
        send('f'); drain()
        for cols, rows in ((120,32), (80,24), (54,18), (40,12)):
            resize(cols, rows)
            frame(f'assessments-{cols}x{rows}')
        resize(80,24)
        send(']'); drain()
        frame('supporting-reference-80x24')
        send('\r'); drain()
        assert 'Timeline' in '\n'.join(screen.display)
        frame('supporting-timeline-80x24')
        send('\x1b'); drain()
        frame('assessment-return-80x24')
        send('\x1b'); drain()
        send('x'); drain()
        for cols, rows in ((120,32), (80,24), (54,18), (40,12)):
            resize(cols, rows)
            frame(f'projections-{cols}x{rows}')
        resize(80,24)
        send('g'); drain()
        send('b'); drain(1)
        send('v'); drain()
        assert 'attempts 2' in '\n'.join(screen.display)
        frame('projection-rebuild-80x24')
        send('a'); drain()
        frame('projection-all-disabled-80x24')
        send('\x1b'); drain()
        send('g' + ']'*3); drain()
        send('d'); drain()
        for cols, rows in ((120,32), (80,24), (54,18), (40,12)):
            resize(cols, rows)
            frame(f'details-{cols}x{rows}')
        resize(80,24)
        send('w' + '\x1b[C'*10); drain()
        frame('unwrapped-80x24')
        send('\x1b'); drain()
        send('g' + ']'*6); drain()
        send('d'); drain()
        assert 'Press u' in '\n'.join(screen.display)
        frame('unknown-explicit-80x24')
        send('u'); drain()
        send('G'); drain()
        assert 'raw stays hidden' in '\n'.join(screen.display)
        frame('unknown-redacted-80x24')
        resize(32,10)
        frame('resize-32x10')
        send('q')
    elif SEARCH:
        def query(text):
            send('/')
            drain(0.15)
            send('\x01')  # Ctrl-A, then Delete at the cursor.
            send('\x1b[3~'*256)
            send('\x1b[200~'+text+'\x1b[201~')
            drain(0.2)

        query('go tst')
        send('\x1b[D'*2+'e'); drain()
        assert 'go te█st' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('editing-120x32')
        send('\x05')  # Ctrl-E.
        send('\x1b[200~ q/?[]\x1b[201~'); drain()
        assert 'q/?[]█' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('paste-120x32')
        send('\x7f'*6); drain()
        send('\r'); drain()
        assert 'Result 1/1' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('results-120x32')
        for cols, rows in ((80,24), (54,18), (40,12)):
            resize(cols, rows)
            assert 'Match' in '\n'.join(screen.display), '\n'.join(screen.display)
            frame(f'results-{cols}x{rows}')
        resize(80,24)
        send('\r'); drain()
        assert 'Timeline' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('opened-80x24')
        send('\x1b'); drain()
        assert 'Query: go test' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('returned-80x24')
        query('absentword'); send('\r'); drain()
        assert 'No matching sessions' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('empty-80x24')
        query('kind:invalid'); send('\r'); drain()
        assert 'Search could not be completed' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('error-80x24')
        query('q/?[]')
        resize(32,10)
        assert 'Ctrl-C quit' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('resize-32x10')
        send('\x03')
    else:
        drain(1.5)
        assert 'PREVIEW' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('sessions-120x32')
        resize(80, 24)
        send('\r'); drain()
        frame('timeline-80x24')
        send('\x1b[6~'); drain()
        frame('reading-80x24')
        send(']]]'); drain()
        send('\r'); drain()
        assert 'RECORDED FAILURE' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('evidence-80x24')
        send('d'); drain()
        frame('inspection-wrapped-80x24')
        send('w'); drain()
        send('\x1b[C'*16); drain()
        assert 'timeout=3000ms' in '\n'.join(screen.display), '\n'.join(screen.display)
        frame('inspection-80x24')
        send('\x1b'); drain()
        send(']]]'); drain()
        send('\r'); drain()
        frame('unknown-80x24')
        send('['*3); drain()
        resize(54, 18)
        frame('compact-54x18')
        resize(40, 12)
        frame('minimum-40x12')
        resize(32, 10)
        frame('resize-32x10')
        send('q')
    child.expect(pexpect.EOF, timeout=5)
    output = ''.join(item[2] for item in recording[1:] if item[1] == 'o')
    assert not re.search(r'\x1b\[\?(?:1000|1002|1003|1006)h', output), 'mouse capture enabled'
    if theme == 'mono':
        assert not re.search(r'\x1b\[[0-9;:]*(?:38|48)[;:]', output), 'color survived NO_COLOR'
    (OUT/('workflow-'+theme+'.cast')).write_text('\n'.join(json.dumps(item) for item in recording)+'\n')


hashes = {p: hashlib.sha256(p.read_bytes()).digest() for p in FIXTURES.glob('*.jsonl')}
for theme in ('dark', 'light', 'mono'):
    capture(theme)
assert all(hashlib.sha256(p.read_bytes()).digest() == digest for p, digest in hashes.items())
if ACCEPTANCE:
    screens = {'sessions':'Sessions', 'timeline':'Conversation', 'expanded':'Expanded event',
               'details':'Inspection', 'search':'Search', 'assessments':'Assessments',
               'unknown-prompt':'Unknown event', 'unknown-raw':'Retained evidence',
               'projections':'Derived views', 'indexing':'Indexing', 'help':'Help'}
    gallery = ['''<!doctype html><html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AgentSession TUI review</title>
<style>
body{font:16px system-ui;background:#f4f5f7;color:#172434;margin:28px;max-width:1180px}
h1{font-size:26px;margin-bottom:6px}a{color:#005fa3}
.controls{display:flex;gap:20px;flex-wrap:wrap;margin:24px 0 16px}
label{display:grid;gap:6px;font-size:14px}select,button{font:inherit;padding:7px 12px}
figure{margin:0}img{display:block;max-width:100%;height:auto;border:1px solid #aab7c5}
figcaption{margin:12px 0;color:#536071}footer{display:flex;gap:20px;flex-wrap:wrap;margin:22px 0}
details{margin:20px 0}summary{cursor:pointer}code{display:block;margin:16px 0}
</style><h1>AgentSession TUI</h1>
<p>Choose a screen, size and color profile to review the second simplification.</p>
<div class="controls"><label>Screen<select id="screen">''']
    for value, label in screens.items():
        gallery.append(f'<option value="{value}">{label}</option>')
    gallery.append('''</select></label>
<label>Terminal size<select id="size"><option>80x24</option><option>120x32</option>
<option>54x18</option><option>40x12</option><option>32x10</option></select></label>
<label>Color profile<select id="theme"><option value="dark">Dark</option>
<option value="light">Light</option><option value="mono">Color-free</option></select></label>
</div><figure><img id="frame" src="sessions-80x24-dark.png" alt="Sessions at 80x24 in dark">
<figcaption><span id="caption">Sessions · 80x24 · dark</span> ·
<a id="native" href="sessions-80x24-dark.png">Full size</a></figcaption></figure>
<div><button id="previous">Previous screen</button> <button id="next">Next screen</button></div>
<footer><a href="../tui-review-88.html">Accepted preview</a>
<a href="README.md">Validation and review record</a>
<a id="recording" href="workflow-dark.cast">Workflow recording</a>
<a href="./">All capture files</a></footer>
<details><summary>Try the live TUI</summary>
<code>go run ./docs/design/tui-89/_capture</code>
<p>Run from the repository root. Browse, read, inspect and return; try search,
resizing and normal terminal text selection. The runner uses isolated sample data.</p></details>
<p>Owner accepted this version on 2026-10-12. See the validation record for review scope and checks.</p>
<noscript><p>Enable JavaScript to use the selectors, or open All capture files above.</p></noscript>
<script>
const screen = document.getElementById('screen');
const size = document.getElementById('size');
const theme = document.getElementById('theme');
function update() {
  const name = size.value === '32x10' ? 'resize' : screen.value;
  const file = name + '-' + size.value + '-' + theme.value + '.png';
  const label = size.value === '32x10' ? 'Resize prompt' : screen.selectedOptions[0].text;
  const caption = label + ' · ' + size.value + ' · ' + theme.selectedOptions[0].text;
  document.getElementById('frame').src = file;
  document.getElementById('frame').alt = caption;
  document.getElementById('caption').textContent = caption;
  document.getElementById('native').href = file;
  document.getElementById('recording').href = 'workflow-' + theme.value + '.cast';
}
for (const control of [screen, size, theme]) control.addEventListener('change', update);
document.getElementById('previous').addEventListener('click', () => {
  screen.selectedIndex = (screen.selectedIndex + screen.length - 1) % screen.length; update();
});
document.getElementById('next').addEventListener('click', () => {
  screen.selectedIndex = (screen.selectedIndex + 1) % screen.length; update();
});
update();
</script>''')
    (OUT/'gallery.html').write_text('\n'.join(gallery)+'</html>\n')
print('Captured production TUI in three terminal profiles; fixture bytes unchanged.')
