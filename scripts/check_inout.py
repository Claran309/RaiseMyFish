import subprocess, os, json
env = dict(os.environ, PGPASSWORD="fish_pass", PGCLIENTENCODING="UTF8")
def q(sql, out):
    subprocess.run(["C:/Program Files/PostgreSQL/17/bin/psql.exe","-h","127.0.0.1","-U","fish","-d","raisemyfish","-t","-A","-F","\t","-o",out,"-c",sql], env=env, capture_output=True)
    return open(out,"rb").read().decode("utf-8","replace")

print("--- IN ---")
for line in q("SELECT to_char(at,'HH24:MI:SS'), from_name, text, status, meta::text FROM events WHERE session='30308220' AND dir='in' ORDER BY at DESC LIMIT 2;", "in1.txt").splitlines():
    p = line.split("\t")
    if len(p) >= 4:
        print(p[0], "|", p[1], "|", p[2][:30], "|", p[3])
        if len(p) > 4:
            try:
                m = json.loads(p[4])
                print("   reply:", {k:v for k,v in m.items() if 'reply' in k.lower()})
            except Exception:
                pass

print("--- OUT ---")
for line in q("SELECT to_char(at,'HH24:MI:SS'), text FROM events WHERE dir='out' AND session='30308220' ORDER BY at DESC LIMIT 3;", "out1.txt").splitlines():
    p = line.split("\t")
    if len(p) >= 2:
        print(p[0], "|", p[1][:50])
