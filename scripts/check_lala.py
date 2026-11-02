import subprocess, os
env = dict(os.environ, PGPASSWORD="fish_pass", PGCLIENTENCODING="UTF8")
def q(sql, out):
    subprocess.run(["C:/Program Files/PostgreSQL/17/bin/psql.exe","-h","127.0.0.1","-U","fish","-d","raisemyfish","-t","-A","-F","\t","-o",out,"-c",sql], env=env, capture_output=True)
    return open(out,"rb").read().decode("utf-8","replace")

lines = []
for line in q("SELECT to_char(at,'HH24:MI:SS'), text FROM events WHERE dir='in' AND session='30308220' AND text LIKE '%拉拉%' ORDER BY at DESC LIMIT 5;", "la1.txt").splitlines():
    p = line.split("\t")
    if len(p) >= 2: lines.append("IN " + p[0] + " | " + p[1])
for line in q("SELECT to_char(at,'HH24:MI:SS'), text FROM events WHERE dir='out' AND session='30308220' AND at > '2026-11-02 22:40:00' ORDER BY at DESC LIMIT 8;", "la2.txt").splitlines():
    p = line.split("\t")
    if len(p) >= 2: lines.append("OUT " + p[0] + " | " + p[1])
open("la_result.txt", "w", encoding="utf-8").write("\n".join(lines) or "(none)")
