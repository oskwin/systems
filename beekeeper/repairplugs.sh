#!/usr/bin/env bash
# repairplugs.sh — Find ZigBee smart plugs with missing power metering and re-pair them.
#
# Usage:
#   ./repairplugs.sh <pi-ip>
#   ./repairplugs.sh <pi-ip> <api-key>
#
# Requirements: curl, python3 (both are standard on macOS, Linux, and Raspberry Pi OS)

set -euo pipefail

# ── Arguments ────────────────────────────────────────────────────────────────

PI_IP="${1:-}"
if [[ -z "$PI_IP" ]]; then
    echo "Usage: $0 <pi-ip> [api-key]"
    exit 1
fi

BASE="http://${PI_IP}/api"

# ── API key ───────────────────────────────────────────────────────────────────
# Use the second argument, or extract from systemconfig.json in the same directory,
# or ask the user.

API_KEY="${2:-}"

if [[ -z "$API_KEY" ]]; then
    SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
    CONFIG="${SCRIPT_DIR}/systemconfig.json"
    if [[ -f "$CONFIG" ]]; then
        API_KEY=$(python3 -c "
import json, sys
with open('$CONFIG') as f:
    cfg = json.load(f)
for ua in cfg.get('unit_assets', []):
    for trait in ua.get('traits', []):
        key = trait.get('apiKey', '')
        if key:
            print(key)
            sys.exit(0)
" 2>/dev/null || true)
    fi
fi

if [[ -z "$API_KEY" ]]; then
    read -rp "deCONZ API key: " API_KEY
fi

API="${BASE}/${API_KEY}"

# ── Connectivity check ────────────────────────────────────────────────────────

echo "Connecting to deCONZ at ${PI_IP} ..."
if ! curl -sf "${API}/config" -o /dev/null; then
    echo "ERROR: cannot reach ${API}/config — check the IP address and API key."
    exit 1
fi
echo "Connected."
echo ""

# ── Fetch lights and sensors ──────────────────────────────────────────────────

LIGHTS=$(curl -sf "${API}/lights")
SENSORS=$(curl -sf "${API}/sensors")

# ── Analyse ───────────────────────────────────────────────────────────────────
# For each plug (light with type containing "plug"), extract its MAC prefix and
# check whether a ZHAPower sensor with the same MAC exists.

ANALYSIS=$(python3 - "$LIGHTS" "$SENSORS" <<'PYEOF'
import json, sys, re

plug_pattern = re.compile(r'plug', re.IGNORECASE)

lights  = json.loads(sys.argv[1])
sensors = json.loads(sys.argv[2])

def mac_prefix(uid):
    return uid.split('-')[0] if uid else ''

# Build a set of MACs that have a ZHAPower sensor.
power_macs = set()
power_sensor_ids = {}   # mac → list of sensor IDs
for sid, s in sensors.items():
    if s.get('type') == 'ZHAPower':
        mac = mac_prefix(s.get('uniqueid', ''))
        power_macs.add(mac)
        power_sensor_ids.setdefault(mac, []).append(sid)

# Find all plugs and report status.
results = []
for lid, l in lights.items():
    if not plug_pattern.search(l.get('type', '')):
        continue
    mac = mac_prefix(l.get('uniqueid', ''))
    has_power = mac in power_macs
    results.append({
        'id':        lid,
        'name':      l.get('name', '(unknown)'),
        'type':      l.get('type', ''),
        'mac':       mac,
        'has_power': has_power,
        'sensor_ids': power_sensor_ids.get(mac, []),
    })

print(json.dumps(results))
PYEOF
)

# ── Report ────────────────────────────────────────────────────────────────────

TOTAL=$(echo "$ANALYSIS" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
MISSING=$(echo "$ANALYSIS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(len([d for d in data if not d['has_power']]))")

echo "Found ${TOTAL} plug(s):"
echo "$ANALYSIS" | python3 -c "
import json, sys
for d in json.load(sys.stdin):
    status = 'OK  (on_off + power)' if d['has_power'] else 'MISSING power sensor'
    print(f\"  [{d['id']}] {d['name']:<25} {status}\")
"
echo ""

if [[ "$MISSING" -eq 0 ]]; then
    echo "All plugs have power metering. Nothing to do."
    exit 0
fi

echo "${MISSING} plug(s) are missing a ZHAPower sensor."
echo ""
read -rp "Attempt to re-pair them now? [y/N] " CONFIRM
if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
    echo "Aborted."
    exit 0
fi

# ── Re-pair each affected plug ────────────────────────────────────────────────

AFFECTED=$(echo "$ANALYSIS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
print(json.dumps([d for d in data if not d['has_power']]))")

echo "$AFFECTED" | python3 -c "import json,sys; [print(d['id']+'|'+d['name']+'|'+d['mac']) for d in json.load(sys.stdin)]" |
while IFS='|' read -r LID NAME MAC; do

    echo "────────────────────────────────────────"
    echo "Re-pairing: ${NAME} (light id=${LID}, mac=${MAC})"
    echo ""

    # 1. Delete the light entry.
    echo "  [1/4] Deleting light entry ${LID} ..."
    curl -sf -X DELETE "${API}/lights/${LID}" -o /dev/null
    echo "        Done."

    # 2. Delete any stale sensor entries with the same MAC.
    STALE=$(curl -sf "${API}/sensors" | python3 -c "
import json, sys
mac = '$MAC'
sensors = json.load(sys.stdin)
ids = [sid for sid, s in sensors.items()
       if s.get('uniqueid','').startswith(mac)]
print(' '.join(ids))")

    if [[ -n "$STALE" ]]; then
        echo "  [2/4] Deleting stale sensor(s): ${STALE} ..."
        for SID in $STALE; do
            curl -sf -X DELETE "${API}/sensors/${SID}" -o /dev/null
            echo "        Deleted sensor ${SID}."
        done
    else
        echo "  [2/4] No stale sensors to clean up."
    fi

    # 3. Start a 3-minute search window.
    echo "  [3/4] Opening pairing window (180 s) ..."
    curl -sf -X POST "${API}/lights" -o /dev/null
    curl -sf -X POST "${API}/sensors" -o /dev/null
    echo "        Pairing window open."
    echo ""
    echo "  >>> Power-cycle ${NAME}: unplug it, wait 5 seconds, plug it back in."
    echo "  >>> The script will wait up to 90 seconds for it to re-join."
    echo ""

    # 4. Poll for the new ZHAPower sensor for up to 90 seconds.
    echo "  [4/4] Waiting for ZHAPower sensor with MAC ${MAC} ..."
    FOUND=0
    for i in $(seq 1 18); do
        sleep 5
        CHECK=$(curl -sf "${API}/sensors" | python3 -c "
import json, sys
mac = '$MAC'
sensors = json.load(sys.stdin)
ids = [sid for sid, s in sensors.items()
       if s.get('type') == 'ZHAPower' and s.get('uniqueid','').startswith(mac)]
print(' '.join(ids))")
        if [[ -n "$CHECK" ]]; then
            FOUND=1
            echo "        ZHAPower sensor appeared: id(s) ${CHECK}"
            break
        fi
        echo "        Still waiting ... (${i}/18)"
    done

    if [[ "$FOUND" -eq 1 ]]; then
        # 5. Restore the original friendly name on the new light entry.
        NEW_LID=$(curl -sf "${API}/lights" | python3 -c "
import json, sys
mac = '$MAC'
lights = json.load(sys.stdin)
ids = [lid for lid, l in lights.items()
       if l.get('uniqueid','').startswith(mac)]
print(ids[0] if ids else '')")

        if [[ -n "$NEW_LID" ]]; then
            echo "  [5/5] Restoring name '${NAME}' on light ${NEW_LID} ..."
            curl -sf -X PUT "${API}/lights/${NEW_LID}" \
                -H "Content-Type: application/json" \
                -d "{\"name\":\"${NAME}\"}" -o /dev/null
            echo "        Done."
        fi
        echo ""
        echo "  SUCCESS: ${NAME} now has power metering."
    else
        echo ""
        echo "  WARNING: ZHAPower sensor did not appear within 90 s."
        echo "  Try power-cycling the plug again and re-running this script,"
        echo "  or move it closer to the Pi while pairing."
    fi
    echo ""

done

echo "────────────────────────────────────────"
echo "Done. Restart beekeeper to pick up the new services."
