import json, subprocess, sys, tempfile, unittest
from pathlib import Path

class CLITests(unittest.TestCase):
    def test_estimate_requires_explicit_consent_and_separate_output(self):
        cli=Path(__file__).with_name('history.py')
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'state';root.mkdir()
            for args in [ ['--out',str(root/'output')], ['--out',str(Path(tmp)/'out'),'--allow-estimates'], ['--out',str(Path(tmp)/'out'),'--activity-index',str(Path(tmp)/'index')] ]:
                r=subprocess.run([sys.executable,str(cli),'--root',str(root),*args],capture_output=True)
                self.assertNotEqual(r.returncode,0)
            self.assertEqual(list(root.iterdir()),[])
    def test_estimated_preview_conserves_numbers_and_does_not_install(self):
        cli=Path(__file__).with_name('history.py')
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'state';(root/'stats').mkdir(parents=True)
            record={'ts':'2026-01-01T00:00:01Z','model':'deepseek/m','prompt':10,'completion':2,'cache_hit':3,'cache_miss':7,'total':12,'requests':1}
            daily=root/'stats/day.jsonl';daily.write_text(json.dumps(record)+'\n');before=daily.read_bytes()
            index=Path(tmp)/'index.json';index.write_text(json.dumps({'owners':[{'id':'s','key':'reasonix:s','path':str(root/'s.jsonl'),'cwd':'','start':'2026-01-01T00:00:00Z','last':'2026-01-01T00:00:02Z','models':['deepseek/m'],'native':True}],'activities':[{'session_id':'s','at':'2026-01-01T00:00:01Z'}]}))
            out=Path(tmp)/'out'
            r=subprocess.run([sys.executable,str(cli),'--root',str(root),'--out',str(out),'--allow-estimates','--activity-index',str(index)],capture_output=True,text=True)
            self.assertEqual(r.returncode,0,r.stderr)
            summary=json.loads(r.stdout);self.assertEqual(summary['totals']['total'],12)
            overlay=json.loads((out/'estimated/reasonix-history-attribution.json').read_text())
            self.assertEqual(overlay['sessions']['s']['deepseek/m']['2026-01-01']['input'],7)
            self.assertEqual(daily.read_bytes(),before)
            self.assertFalse((root/'reasonix-history-attribution.json').exists())
if __name__=='__main__':unittest.main()
