import json, tempfile, unittest
from pathlib import Path
from reconcile import reconcile

class ReconciliationTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)/'root';self.root.mkdir();self.out=Path(self.tmp.name)/'out'
    def tearDown(self): self.tmp.cleanup()
    def ledger(self, folder='a', sid='s1', prompt=10):
        p=self.root/folder;p.mkdir()
        (p/'old.jsonl').write_text('[]')
        (p/'old.jsonl.meta').write_text(json.dumps({'id':sid}))
        e={'sessionId':sid,'seq':1,'createdAt':1000,'kind':'usage','recordType':'event','event':{'usage':{'model':'deepseek/m','promptTokens':prompt,'completionTokens':2,'cacheHitTokens':3,'cacheMissTokens':7,'totalTokens':12,'costQuote':{'original':{'amount':'0.123456789','currency':'USD'},'costComplete':True,'estimated':False}}}}
        (p/'old.turns.jsonl').write_text(json.dumps(e)+'\n');return p
    def daily(self, ts='1970-01-01T00:00:01Z'):
        p=self.root/'stats';p.mkdir();(p/'1970-01-01.jsonl').write_text(json.dumps({'ts':ts,'model':'deepseek/m','prompt':10,'completion':2,'cache_hit':3,'cache_miss':7,'total':12,'requests':1})+'\n')
    def test_duplicate_not_counted_twice(self):
        self.ledger();self.ledger('b');self.daily();s=reconcile(self.root,self.out)
        self.assertEqual(s['verified_ledger_receipts'],1);self.assertEqual(s['duplicate_ledger_rows_removed'],1);self.assertEqual(s['exact_daily_rows_attributed'],1)
        a=json.loads((self.out/'session-allocations.json').read_text())[0];self.assertEqual(a['quoted_original_amounts'],{'USD':'0.123456789'})
    def test_ambiguous_identity_not_allocated(self):
        self.ledger();self.ledger('b','s2');self.daily();s=reconcile(self.root,self.out)
        self.assertEqual(s['exact_daily_rows_attributed'],0);self.assertEqual(s['ambiguous_exact_matches'],1)
    def test_fractional_timezone_timestamp_uses_integer_milliseconds(self):
        self.ledger();self.daily('1970-01-01T08:00:01.00044+08:00')
        s=reconcile(self.root,self.out)
        self.assertEqual(s['exact_daily_rows_attributed'],1)
    def test_near_timestamp_not_allocated(self):
        self.ledger();self.daily('1970-01-01T00:00:01.001Z');s=reconcile(self.root,self.out);self.assertEqual(s['exact_daily_rows_attributed'],0)
    def test_owner_mismatch_rejected(self):
        p=self.ledger();(p/'old.jsonl.meta').write_text(json.dumps({'id':'other'}));self.daily();s=reconcile(self.root,self.out)
        self.assertEqual(s['verified_ledger_receipts'],0);self.assertEqual(s['rejected']['owner_identity_mismatch'],1)
    def test_conflicting_identity_rejected(self):
        self.ledger();self.ledger('b',prompt=11);self.daily();s=reconcile(self.root,self.out)
        self.assertEqual(s['verified_ledger_receipts'],0);self.assertEqual(s['rejected']['conflicting_receipt_identity'],1)
    def test_source_unchanged(self):
        self.ledger();self.daily();before={str(p):p.read_bytes() for p in self.root.rglob('*') if p.is_file()};reconcile(self.root,self.out)
        self.assertEqual(before,{str(p):p.read_bytes() for p in self.root.rglob('*') if p.is_file()})
if __name__=='__main__': unittest.main()
