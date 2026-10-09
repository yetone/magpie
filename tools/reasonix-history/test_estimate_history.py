import unittest
from estimate_history import rank_candidates

class AttributionPolicyTests(unittest.TestCase):
    def owner(self, models, start=0, last=100000):
        return {'models_normalized':set(models),'start_ms':start,'last_ms':last}
    def test_matching_model_beats_wrong_model_at_same_time(self):
        owners={'flash':self.owner(['flash']),'pro':self.owner(['pro'])}
        got=rank_candidates(owners,{'flash':[1000],'pro':[1000]},1000,'flash')
        self.assertEqual(got[0][2],'flash');self.assertGreater(got[1][0],got[0][0])
    def test_nearest_active_session_wins(self):
        owners={'old':self.owner(['m']),'active':self.owner(['m'])}
        got=rank_candidates(owners,{'old':[0],'active':[9500]},10000,'m')
        self.assertEqual(got[0][2],'active')
    def test_overlap_tie_keeps_both_candidates_and_stable_order(self):
        owners={'b':self.owner(['m']),'a':self.owner(['m'])}
        got=rank_candidates(owners,{'b':[1000],'a':[1000]},1000,'m')
        self.assertEqual([x[2] for x in got],['a','b']);self.assertEqual(got[0][0],got[1][0])
    def test_lifetime_only_has_weaker_evidence(self):
        got=rank_candidates({'s':self.owner(['m'])},{'s':[]},1000,'m')
        self.assertEqual(got[0][3],'session-lifetime-only')
if __name__=='__main__':unittest.main()
