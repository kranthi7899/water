import os
G = {
 'W': ["10001","10001","10001","10101","10101","10101","01010"],
 'A': ["01110","10001","10001","11111","10001","10001","10001"],
 'T': ["11111","00100","00100","00100","00100","00100","00100"],
 'E': ["11111","10000","10000","11110","10000","10000","11111"],
 'R': ["11110","10001","10001","11110","10100","10010","10001"],
}
word = "WATER"; C = 36; GAP = 1
cols = len(word) * 5 + (len(word) - 1) * GAP
CW, CH = 1280, 340
x0 = (CW - cols * C) // 2; y0 = 40
cells = set()
for i, ch in enumerate(word):
    ox = i * (5 + GAP)
    for r, row in enumerate(G[ch]):
        for c, v in enumerate(row):
            if v == '1':
                cells.add((ox + c, r))
clip = ''.join(f"M{x0+c*C} {y0+r*C}h{C}v{C}h-{C}z" for c, r in sorted(cells))
seg = []
for c, r in sorted(cells):
    x = x0 + c * C; y = y0 + r * C
    if (c, r-1) not in cells: seg.append(f"M{x} {y}h{C}")
    if (c, r+1) not in cells: seg.append(f"M{x} {y+C}h{C}")
    if (c-1, r) not in cells: seg.append(f"M{x} {y}v{C}")
    if (c+1, r) not in cells: seg.append(f"M{x+C} {y}v{C}")
edges = ''.join(seg)
H = 7 * C; b1 = y0 + round(H * 0.4); b2 = y0 + round(H * 0.8)
TOP, MID, BOT, LINE, PANEL = '#6CCBFF', '#3B94F2', '#2D5FB8', '#4A9FD1', '#0E141C'
svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{CW}" height="{CH}" viewBox="0 0 {CW} {CH}" role="img" aria-label="WATER">
<title>WATER</title>
<defs>
<clipPath id="letters"><path d="{clip}"/></clipPath>
<path id="edges" d="{edges}" fill="none" stroke-linecap="square"/>
</defs>
<rect width="{CW}" height="{CH}" rx="8" fill="{PANEL}"/>
<use href="#edges" transform="translate(16 16)" stroke="{LINE}" stroke-opacity="0.45" stroke-width="1.5"/>
<use href="#edges" transform="translate(8 8)" stroke="{LINE}" stroke-opacity="0.85" stroke-width="1.5"/>
<g clip-path="url(#letters)">
<rect x="0" y="0" width="{CW}" height="{b1}" fill="{TOP}"/>
<rect x="0" y="{b1}" width="{CW}" height="{b2-b1}" fill="{MID}"/>
<rect x="0" y="{b2}" width="{CW}" height="{CH-b2}" fill="{BOT}"/>
</g>
</svg>
'''
os.makedirs('docs/assets', exist_ok=True)
open('docs/assets/water-banner.svg', 'w').write(svg)
print('wrote docs/assets/water-banner.svg')
