"""User-authorized approximate ownership; never rewrite producer histories."""
import bisect, collections, datetime, decimal, hashlib, json
from pathlib import Path
from reconcile import FIELDS, vector, rows

def ms(value):
    if not value or str(value).startswith('0001'): return None
    d=datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
    delta=d-datetime.datetime(1970,1,1,tzinfo=datetime.timezone.utc)
    return (delta.days*86400+delta.seconds)*1000+delta.microseconds//1000

def model(value):
    return str(value).removeprefix('magpie/').replace('deepseek-v4-flash','deepseek-flash').replace('deepseek-v4-pro','deepseek-pro').split('/')[-1]

def rank_candidates(owners, points, at, wanted):
    candidates=[]
    for identity,o in owners.items():
        ps=points[identity];a=o['start_ms'];b=o['last_ms']
        if not ps and a is None and b is None:continue
        compatible=not o['models_normalized'] or wanted in o['models_normalized']
        if ps:
            pos=bisect.bisect_left(ps,at);d=min(abs(x-at) for x in ps[max(0,pos-1):pos+1]);evidence='native-or-ledger-activity'
            score=d
        else:
            ends=[x for x in [a,b] if x is not None];d=min(abs(at-x) for x in ends)
            inside=a is not None and b is not None and a<=at<=b
            score=min(d,300000) if inside else d+300000;evidence='session-lifetime-only'
        if not compatible:score+=24*3600000
        candidates.append((score,d,identity,evidence,compatible))
    candidates.sort()
    return candidates

def estimate(root, base, activity_index):
    ROOT=root
    BASE=base
    source=json.loads(activity_index.read_text())
    owners={o['id']:o for o in source['owners']};points=collections.defaultdict(list);aliases={}
    allocations=json.loads((BASE/'attribution/session-allocations.json').read_text())
    for a in allocations:
        targets=[v for v in a['canonical_import_targets'] if v in owners]
        if len(targets)==1:aliases[a['session_id']]=targets[0]
    def destination(sid):return aliases.get(sid,sid)
    for a in source['activities']:
        at=ms(a['at'])
        if at is not None:points[a['session_id']].append(at)
    for p in ROOT.rglob('*.turns.jsonl'):
        try:sid=json.loads(Path(str(p).replace('.turns.jsonl','.jsonl.meta')).read_text())['id']
        except (KeyError,OSError,ValueError):continue
        sid=destination(sid)
        if sid not in owners:continue
        for _,v,_ in rows(p):
            at=v.get('createdAt')
            if isinstance(at,int) and at>0:points[sid].append(at)
    for sid,o in owners.items():
        if not o['native']:
            try:
                meta=json.loads(Path(o['path']+'.meta').read_text())
                o['start']=meta.get('created_at') or o['start'];o['last']=meta.get('updated_at') or o['last']
                if meta.get('model'):o['models']=(o['models'] or [])+[meta['model']]
            except (OSError,ValueError):pass
        points[sid]=sorted(set(points[sid]));o['models_normalized']={model(m) for m in (o['models'] or [])}
        for name in ['flash','pro']:
            if name in Path(o['path']).name.lower():o['models_normalized'].add('deepseek-'+name)
        o['start_ms']=ms(o['start']);o['last_ms']=ms(o['last'])
    exact={(v['path'],v['line']):v for v in json.loads((BASE/'attribution/attributed-daily-rows.json').read_text())}
    ledger=collections.defaultdict(list)
    for r in json.loads((BASE/'attribution/verified-receipts.json').read_text()):
        ledger[vector(r['usage'],True)].append(r)
    records=[];summaries={};confidence=collections.Counter();methods=collections.Counter()
    for p in sorted((ROOT/'stats').glob('*.jsonl')):
        for line,v,digest in rows(p):
            if v.get('turn') or (not v.get('total') and not v.get('requests')):continue
            if any(type(v.get(k,0)) is not int or v.get(k,0)<0 for k,_ in FIELDS[1:]) or v.get('cache_hit',0)>v.get('prompt',0):
                raise ValueError('invalid daily token counters')
            at=ms(v['ts'])
            if at is None or not v.get('model'):raise ValueError('daily record requires timestamp and model')
            known=exact.get((str(p),line));sid=None;alternatives=[];distance=None
            if known and destination(known['session_id']) in owners:
                sid=destination(known['session_id']);level='measured';method='verified-ledger-and-exact-daily-vector'
            else:
                same=ledger.get(vector(v),[])
                close=[r for r in same if abs(r['created_at_ms']-at)<=1000 and destination(r['session_id']) in owners]
                if len(close)==1:
                    sid=destination(close[0]['session_id']);level='high';method='unique-usage-vector-ledger-within-1s';distance=abs(close[0]['created_at_ms']-at)
            if sid is None:
                wanted=model(v.get("model"))
                candidates=rank_candidates(owners,points,at,wanted)
                candidates.sort()
                if not candidates:raise ValueError('no session activity candidates; use exact-only reconciliation')
                best=candidates[0];score,distance,sid,evidence,compatible=best
                alternatives=[{'session_id':c[2],'score_ms':c[0]} for c in candidates[1:4] if c[0]-score<=300000]
                gap=candidates[1][0]-score if len(candidates)>1 else None
                level='high' if evidence=='native-or-ledger-activity' and distance<=30000 and compatible and (gap is None or gap>10000) else 'medium' if evidence=='native-or-ledger-activity' and distance<=300000 and compatible else 'low'
                method=evidence+'+model+nearest-time'
            proof={'path':str(p),'line':line,'sha256':digest,'session_id':sid,'timestamp':v['ts'],'confidence':level,'estimated_attribution':level!='measured','method':method,'distance_ms':distance,'alternatives':alternatives,'record':v}
            records.append(proof);confidence[level]+=1;methods[method]+=1
            o=owners[sid];s=summaries.setdefault(sid,{'session_id':sid,'magpie_key':o['key'],'source_path':o['path'],'cwd':o['cwd'],'records':0,'confidence_counts':{},'models':{},'original_quote_amounts':{},'estimated_attribution':False})
            s['records']+=1;s['estimated_attribution']|=level!='measured';s['confidence_counts'][level]=s['confidence_counts'].get(level,0)+1
            day=v['ts'][:10];ref=v.get('model','');entry=s['models'].setdefault(ref,{'days':{}});tot=entry['days'].setdefault(day,{k:0 for k,_ in FIELDS[1:]})
            for k,_ in FIELDS[1:]:tot[k]+=v.get(k,0)
            if v.get('cost_amount') is not None and v.get('cost_currency'):
                c=v['cost_currency'];s['original_quote_amounts'][c]=str(decimal.Decimal(s['original_quote_amounts'].get(c,'0'))+decimal.Decimal(str(v['cost_amount'])))
    totals={k:sum(r['record'].get(k,0) for r in records) for k,_ in FIELDS[1:]}
    assert len({(r['path'],r['line']) for r in records})==len(records)
    assert all(sum(s['confidence_counts'].values())==s['records'] for s in summaries.values())
    for k in totals:assert totals[k]==sum(day[k] for s in summaries.values() for m in s['models'].values() for day in m['days'].values())
    out=BASE/'estimated';out.mkdir(exist_ok=True);out.chmod(0o700)
    summary={'schema':1,'policy':'user-authorized approximate historical attribution','daily_records':len(records),'assigned_records':len(records),'sessions_with_attribution':len(summaries),'confidence_counts':dict(confidence),'methods':dict(methods),'totals':totals,'unassigned':0,'app_installed':False,'warning':'Attribution can be approximate; token and quote numbers are original daily records. Do not add ledger/gateway totals. Low confidence assignments are revisable.'}
    for name,data in [('session-allocations.json',list(summaries.values())),('attributed-records.json',records),('summary.json',summary)]:
        p=out/name;p.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n');p.chmod(0o600)
    overlay={'schema':1,'state_home':str(ROOT.resolve()),'covered_dates':sorted({r['timestamp'][:10] for r in records}),'sessions':{},'provenance':{'estimated':True,'confidence_counts':dict(confidence),'not_invoice':True,'snapshot_only':True}}
    for sid,s in summaries.items():
        overlay['sessions'][sid]={ref:{day:{'input':v['prompt']-v['cache_hit'],'output':v['completion'],'cache_read':v['cache_hit'],'cache_write':0} for day,v in m['days'].items()} for ref,m in s['models'].items()}
    p=out/'reasonix-history-attribution.json';p.write_text(json.dumps(overlay,indent=2)+'\n');p.chmod(0o600)
    return summary

