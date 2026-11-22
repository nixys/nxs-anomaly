"""Run from repository root against the disposable local-audit Docker database."""
import json
import statistics
import subprocess
import time
import urllib.request
from pathlib import Path

COMPOSE = ['docker', 'compose', '-p', 'anomaly-audit', '-f', 'deploy/local-audit/compose.yaml']
def sql(statement):
    subprocess.run(COMPOSE + ['exec', '-T', 'postgres', 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'audit', '-d', 'anomaly_audit'], input=statement, text=True, check=True, stdout=subprocess.DEVNULL)
def get(params):
    started = time.perf_counter()
    req = urllib.request.Request('http://127.0.0.1:18080/api/v1/users?' + params, headers={'X-API-Key': 'e2e-key'})
    with urllib.request.urlopen(req) as response:
        body = response.read()
    return json.loads(body), len(body), (time.perf_counter()-started)*1000

results = []
try:
    for size in (1000, 10000, 50000):
        sql("DELETE FROM nxs_anomaly_users WHERE id LIKE 'audit-bench-%';")
        sql(f"""INSERT INTO nxs_anomaly_users (id,data,username,email)
        SELECT 'audit-bench-'||n, jsonb_build_object('id','audit-bench-'||n,'name','Audit benchmark '||n,'username','audit-bench-'||n,'email','audit-bench-'||n||'@example.org'), 'audit-bench-'||n,'audit-bench-'||n||'@example.org' FROM generate_series(1,{size}) n;
        ANALYZE nxs_anomaly_users;""")
        row = {'records': size}
        for label, params in [('page50','limit=50'),('search_last','limit=50&q=audit-bench-'+str(size)+'%40example.org')]:
            samples = [get(params) for _ in range(5)]
            row[label] = {'median_ms':round(statistics.median(x[2] for x in samples),2), 'bytes':samples[-1][1], 'items':len(samples[-1][0]['items'])}
            assert len(samples[-1][0]['items']) == (50 if label=='page50' else 1)
        offset = total_bytes = requests = 0
        started = time.perf_counter()
        while True:
            data, count, _ = get(f'limit=1000&offset={offset}')
            requests += 1
            total_bytes += count
            offset += len(data['items'])
            if len(data['items']) < 1000:
                break
        row['full_download'] = {'ms': round((time.perf_counter()-started)*1000,2), 'bytes':total_bytes,'requests':requests,'items':offset}
        results.append(row)
finally:
    sql("DELETE FROM nxs_anomaly_users WHERE id LIKE 'audit-bench-%';")
output = Path('../ui-audit/docker/benchmark.json')
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps(results, indent=2)+'\n')
print(output.read_text())
