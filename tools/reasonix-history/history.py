"""Offline recovery preview. Python 3.11+, standard library only."""
import argparse, json, sys
from pathlib import Path
from reconcile import reconcile
from estimate_history import estimate

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=Path,required=True,help='Reasonix state root (read only)')
    p.add_argument('--out',type=Path,required=True,help='new private output directory')
    p.add_argument('--activity-index',type=Path,help='reviewed owners/activities JSON; required for estimates')
    p.add_argument('--allow-estimates',action='store_true',help='explicitly permit heuristic ownership; never changes token values')
    a=p.parse_args()
    if sys.version_info<(3,11):p.error('Python 3.11+ is required for producer timestamps')
    root=a.root.resolve();out=a.out.resolve()
    if not root.is_dir():p.error('state root does not exist')
    if out.exists() or out.is_relative_to(root) or root.is_relative_to(out):p.error('output must be new and separate from source')
    if a.allow_estimates and not a.activity_index:p.error('--allow-estimates requires --activity-index')
    if a.activity_index and not a.allow_estimates:p.error('activity index requires explicit --allow-estimates')
    out.mkdir(parents=True,mode=0o700)
    result=reconcile(root,out/'attribution')
    if a.allow_estimates:result=estimate(root,out,a.activity_index)
    print(json.dumps(result))
if __name__=='__main__':main()
