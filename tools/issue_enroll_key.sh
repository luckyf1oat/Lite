#!/bin/sh
# 在 VPS 上执行：签发一条永不失效的 enroll 密钥并打印部署指令。
set -e
DB=/opt/lite/data/lite.db
EP=https://tz3.5671234.xyz

TMPKEY=$(head -c 48 /dev/urandom | base64 | tr -dc '0-9A-Za-z' | head -c 32)
sqlite3 "$DB" "insert into configs(key,value) values('api_key','\"$TMPKEY\"') on conflict(key) do update set value='\"$TMPKEY\"';"
systemctl restart lite
sleep 9

# 撤销所有旧的未撤销密钥，避免多个密钥同时可用
for id in $(sqlite3 "$DB" "select id from enrollment_keys where revoked_at is null;"); do
  curl -s -o /dev/null -X POST http://127.0.0.1:27777/api/rpc2 \
    -H "Authorization: Bearer $TMPKEY" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"admin:revokeEnrollmentKey\",\"params\":{\"id\":\"$id\"}}"
done

echo "=== 签发永不过期的密钥（expires_in_hours=0）==="
curl -s -m 20 -X POST http://127.0.0.1:27777/api/rpc2 \
  -H "Authorization: Bearer $TMPKEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"admin:createEnrollmentKey","params":{"name":"一键部署-永久","max_uses":100,"expires_in_hours":0,"endpoint":"https://tz3.5671234.xyz","interval":600}}' \
  > /tmp/enroll_resp.json
python3 - <<'PY'
import json
r = json.load(open('/tmp/enroll_resp.json'))['result']
print('  prefix       :', r['prefix'])
print('  never_expires:', r['never_expires'])
print('  expires_at   :', r['expires_at'])
print('  max_uses     :', r['max_uses'])
print()
print('  部署指令:')
print(r['command'])
PY
rm -f /tmp/enroll_resp.json

# 清除临时 API key
sqlite3 "$DB" "delete from configs where key='api_key';"
systemctl restart lite
sleep 8
echo ""
echo "api_key 已清: $(sqlite3 "$DB" "select case when value is null or value='' or value='\"\"' then 'YES' else 'NO' end from configs where key='api_key';")"
echo "enroll_enabled: $(sqlite3 "$DB" "select value from configs where key='enroll_enabled';")"
