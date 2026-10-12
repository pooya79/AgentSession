#!/usr/bin/env python3
"""Review-only PTY capture. Requires pexpect, pyte and Pillow; no app dependency."""
import hashlib
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
OUT = Path(__file__).resolve().parent
if SEARCH:
    OUT = OUT.parent/'tui-91'
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
    recording = [{'version': 2, 'width': 120, 'height': 32, 'title': 'AgentSession ' + ('#91 search ' if SEARCH else '#89 ') + theme}]
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
    if SEARCH:
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
        assert 'matching event' in '\n'.join(screen.display), '\n'.join(screen.display)
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
    if theme == 'mono':
        output = ''.join(item[2] for item in recording[1:] if item[1] == 'o')
        assert not re.search(r'\x1b\[[0-9;:]*(?:38|48)[;:]', output), 'color survived NO_COLOR'
    (OUT/('workflow-'+theme+'.cast')).write_text('\n'.join(json.dumps(item) for item in recording)+'\n')


hashes = {p: hashlib.sha256(p.read_bytes()).digest() for p in FIXTURES.glob('*.jsonl')}
for theme in ('dark', 'light', 'mono'):
    capture(theme)
assert all(hashlib.sha256(p.read_bytes()).digest() == digest for p, digest in hashes.items())
print('Captured production TUI in three terminal profiles; fixture bytes unchanged.')
