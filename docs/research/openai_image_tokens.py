# SPDX-License-Identifier: Apache-2.0
# The output token count of an OpenAI image, from the official calculator.
import math
c={"gpt-image-2":{"low":16,"medium":48,"high":96},"gpt-image-2.5":{"low":16,"medium":24,"high":48,"xhigh":64,"max":96}}
def tokens(model,w,h,q):
    i=max(w,h);a=min(w,h);o=c[model][q]
    s=o/(i/a);l=math.floor(s)
    u = l+l%2 if abs(s-l-0.5)<1e-12 else round(s)
    # JS Math.round: half up
    if abs(s-l-0.5)>1e-12: u=math.floor(s+0.5)
    d=(o if w>=h else u)*(u if w>=h else o)
    return math.ceil(d*(2e6+w*h)/4e6)
sizes=[(1024,1024),(1536,1024),(1024,1536),(2048,2048),(2048,1152),(3840,2160)]
for m in c:
    print(m)
    for q in c[m]:
        row=[]
        for w,h in sizes:
            t=tokens(m,w,h,q); row.append(f"{w}x{h}: {t} tok ${t*30/1e6:.4f}")
        print(' ',q,' | '.join(row))
