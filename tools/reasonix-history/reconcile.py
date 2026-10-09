"""Read-only reconciliation. Derived files never replace producer histories."""
import argparse, collections, datetime, decimal, hashlib, json
from pathlib import Path

FIELDS = [('model','model'),('prompt','promptTokens'),('completion','completionTokens'),('cache_hit','cacheHitTokens'),('cache_miss','cacheMissTokens'),('reasoning','reasoningTokens'),('total','totalTokens')]
COST = ['cost_amount','cost_currency','cost_complete','cost_estimated','display_complete','selected_amount','selected_currency','pricing_fingerprint','rate_date','rate_band','rated_at','usage_source']

def vector(v, ledger=False):
    return tuple(str(v.get(b if ledger else a,'' )).removeprefix('magpie/') if a=='model' else v.get(b if ledger else a,0) for a,b in FIELDS)

def rows(p):
    with p.open(errors='replace') as f:
        for n,line in enumerate(f,1):
            try: v=json.loads(line)
            except ValueError: continue
            if isinstance(v,dict): yield n,v,hashlib.sha256(line.encode()).hexdigest()

def exact_join(v, index):
    try:
        dt=datetime.datetime.fromisoformat(v['ts'].replace('Z','+00:00'))
        if dt.tzinfo is None: return []
        delta=dt-datetime.datetime(1970,1,1,tzinfo=datetime.timezone.utc)
        at=(delta.days*86400+delta.seconds)*1000+delta.microseconds//1000
    except (KeyError,ValueError,TypeError): return []
    return index.get((at,vector(v)),[])

def reconcile(root,out):
    by_identity={};conflicts=set();rejected=collections.Counter();aliases=collections.defaultdict(set)
    # Explicit imported source relationships, never inferred from titles.
    for p in root.rglob('manifest.json'):
        try: m=json.loads(p.read_text());src=m.get('source',{});sp=Path(src['path']);meta=json.loads(Path(str(sp)+'.meta').read_text())
        except (ValueError,KeyError,OSError,TypeError): continue
        if m.get('sessionId') and meta.get('id') and sp.resolve().is_relative_to(root.resolve()):
            aliases[meta['id']].add(m['sessionId'])
    for p in root.rglob('*.turns.jsonl'):
        transcript=p.with_name(p.name.removesuffix('.turns.jsonl')+'.jsonl')
        try: meta=json.loads(Path(str(transcript)+'.meta').read_text());sid=meta['id']
        except (OSError,ValueError,KeyError): rejected['ledger_without_valid_owner_metadata']+=1;continue
        for line,e,digest in rows(p):
            if e.get('kind')!='usage' or e.get('recordType')!='event': continue
            u=e.get('event',{}).get('usage')
            if not isinstance(u,dict): continue
            if e.get('sessionId')!=sid: rejected['owner_identity_mismatch']+=1;continue
            if not isinstance(e.get('seq'),int) or e['seq']<=0 or not isinstance(e.get('createdAt'),int):rejected['invalid_identity_or_time']+=1;continue
            key=(sid,e['seq']);signature=json.dumps(e,sort_keys=True,separators=(',',':'))
            if key in by_identity:
                if by_identity[key]['signature']!=signature: conflicts.add(key)
                else: by_identity[key]['evidence'].append({'path':str(p),'line':line,'sha256':digest})
                continue
            by_identity[key]={'signature':signature,'receipt_id':hashlib.sha256((sid+'\0'+str(e['seq'])).encode()).hexdigest(),'session_id':sid,'event_seq':e['seq'],'turn_id':e.get('turnId'),'created_at_ms':e['createdAt'],'usage':u,'evidence':[{'path':str(p),'line':line,'sha256':digest}], 'daily_records':[]}
    index=collections.defaultdict(list);sessions={};receipts=[]
    for key,r in by_identity.items():
        if key in conflicts:rejected['conflicting_receipt_identity']+=1;continue
        u=r['usage'];v=vector(u,True)
        if u.get('estimated') or not v[0] or any(not isinstance(n,int) or n<0 for n in v[1:]) or u.get('cacheHitTokens',0)>u.get('promptTokens',0):rejected['estimated_or_invalid_usage']+=1;continue
        index[(r['created_at_ms'],v)].append(r);r.pop('signature');receipts.append(r)
        s=sessions.setdefault(r['session_id'],{'session_id':r['session_id'],'canonical_import_targets':sorted(aliases[r['session_id']]),'canonical_relation':'explicit source.path + legacy metadata id; imported history relationship, not a new request receipt','usage_incomplete':True,'receipt_count':0,'prompt':0,'completion':0,'cache_hit':0,'cache_miss':0,'reasoning':0,'total':0,'daily_records_linked':0,'quoted_original_amounts':{},'quoted_complete_receipts':0,'quoted_estimated_receipts':0,'quoted_incomplete_receipts':0,'missing_quote_receipts':0})
        s['receipt_count']+=1
        q=u.get('costQuote') or {}; original=q.get('original') or {}
        try:
            amount=decimal.Decimal(str(original['amount'])); currency=original['currency']
            if not amount.is_finite() or amount<0 or not isinstance(currency,str) or not currency: raise ValueError('invalid quote')
            previous=decimal.Decimal(s['quoted_original_amounts'].get(currency,'0'))
            s['quoted_original_amounts'][currency]=str(previous+amount)
            complete=bool(q.get('costComplete',q.get('complete',False)))
            s['quoted_complete_receipts' if complete else 'quoted_incomplete_receipts']+=1
            s['quoted_estimated_receipts']+=bool(q.get('estimated',False))
        except (KeyError,ValueError,decimal.InvalidOperation,TypeError): s['missing_quote_receipts']+=1
        for a,b in FIELDS[1:]:s[a]+=u.get(b,0)
    unmatched=[];matched=[];multi=0;total=0;usage_total=0
    for p in sorted((root/'stats').glob('*.jsonl')):
        for line,v,digest in rows(p):
            total+=1
            if v.get('turn') or (not v.get('total') and not v.get('requests')):continue
            usage_total+=1
            candidates=exact_join(v,index)
            proof={'path':str(p),'line':line,'sha256':digest,'timestamp':v.get('ts'),'usage_vector':list(vector(v))}
            if len(candidates)!=1:
                proof['reason']='ambiguous exact receipt' if len(candidates)>1 else 'no exact receipt identity link'
                unmatched.append(proof);multi+=len(candidates)>1;continue
            r=candidates[0];proof.update(session_id=r['session_id'],receipt_id=r['receipt_id'],method='full usage vector + identical Unix millisecond + unique retained ledger receipt; owner metadata matches ledger sessionId',cost={k:v[k] for k in COST if k in v})
            r['daily_records'].append(proof);sessions[r['session_id']]['daily_records_linked']+=1;matched.append(proof)
    summary={'schema':1,'stats_records_read':total,'stats_usage_records':usage_total,'exact_daily_rows_attributed':len(matched),'unattributed_usage_rows':len(unmatched),'ambiguous_exact_matches':multi,'verified_sessions':len(sessions),'verified_ledger_receipts':len(receipts),'ledger_receipts_with_original_quote':sum(s['receipt_count']-s['missing_quote_receipts'] for s in sessions.values()),'duplicate_ledger_rows_removed':sum(len(r['evidence'])-1 for r in receipts),'rejected':dict(rejected),'complete':not unmatched,'app_installed':False,'warning':'Ledger totals and matching daily rows represent the same consumption. Do not add them. All sessions remain partial; no time-nearest, price, title or text-length inference.'}
    out.mkdir(parents=True,exist_ok=True);out.chmod(0o700)
    for name,data in [('session-allocations.json',list(sessions.values())),('verified-receipts.json',receipts),('attributed-daily-rows.json',matched),('unattributed-daily-rows.json',unmatched),('reconciliation-summary.json',summary)]:
        p=out/name;p.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n');p.chmod(0o600)
    return summary
if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('--root',type=Path,required=True);ap.add_argument('--out',type=Path,required=True);a=ap.parse_args();print(json.dumps(reconcile(a.root,a.out)))
