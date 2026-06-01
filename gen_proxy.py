import base64, re, json, yaml
from urllib.request import urlopen, Request

API_URL = "https://liangxin.xyz/api/v1/liangxin?OwO=32e0b21438d4b570e41a9c9a7ef4aa5d"

def gen_clash(uri):
    """Convert VLESS URI to clash format"""
    if not uri.startswith("vless://"):
        return None
    rest = uri[8:]
    at_idx = rest.index("@")
    uuid = rest[:at_idx]
    rest = rest[at_idx+1:]
    qm = rest.index("?") if "?" in rest else len(rest)
    host_port = rest[:qm]
    if "#" in host_port:
        host_port = host_port[:host_port.index("#")]
    addr = host_port.split(":")[0]
    port = int(host_port.split(":")[1]) if ":" in host_port else 443

    params = {}
    if "?" in rest:
        qs = rest[rest.index("?")+1:]
        if "#" in qs:
            qs = qs[:qs.index("#")]
        for p in qs.split("&"):
            if "=" in p:
                k, v = p.split("=", 1)
                params[k] = v

    name = "proxy"
    if "#" in rest:
        name = rest[rest.index("#")+1:]
        name = re.sub(r'%([0-9A-Fa-f]{2})', lambda m: chr(int(m.group(1),16)), name)

    node = {
        "name": name[:60],
        "type": "vless",
        "server": addr,
        "port": port,
        "uuid": uuid,
    }

    net = params.get("net", params.get("type", "tcp"))
    node["network"] = net

    if params.get("security") == "tls" or params.get("security") == "reality":
        node["tls"] = True
        node["client-fingerprint"] = "chrome"
        node["skip-cert-verify"] = True

    if params.get("sni"):
        node["servername"] = params["sni"]

    if net == "ws" or params.get("type") == "ws":
        ws_opts = {}
        if params.get("path"):
            ws_opts["path"] = params["path"]
        if params.get("host"):
            ws_opts["headers"] = {"Host": params["host"]}
        if ws_opts:
            node["ws-opts"] = ws_opts

    if params.get("flow"):
        node["flow"] = params["flow"]

    if params.get("security") == "reality":
        node["reality-opts"] = {"public-key": params.get("pbk", ""), "short-id": params.get("sid", "")}
        if params.get("fp"):
            node["client-fingerprint"] = params["fp"]

    return node


def is_jp_or_us(uri):
    sni = ""
    name = ""
    if "sni=" in uri:
        sni = uri.split("sni=")[1].split("&")[0].lower()
    if "host=" in uri:
        h = uri.split("host=")[1].split("&")[0].lower()
        if any(k in h for k in ["jp", "japan", "us", "america", "united"]):
            sni = h
    if "#" in uri:
        name = uri.split("#")[1]
        name = re.sub(r'%([0-9A-Fa-f]{2})', lambda m: chr(int(m.group(1),16)), name).lower()

    jp_kw = ["jp", "japan", "tokyo", "osaka", "\U0001f1ef"]
    us_kw = ["us", "usa", "united", "america", "\U0001f1fa", "los angeles", "new york"]

    return (any(k in sni or k in name for k in jp_kw) or
            any(k in sni or k in name for k in us_kw))


# Fetch
print("Fetching proxies...")
req = Request(API_URL, headers={"User-Agent": "curl/8"})
raw = urlopen(req, timeout=20).read().decode()
try:
    decoded = base64.b64decode(raw).decode()
except:
    decoded = raw

nodes = []
for line in decoded.strip().split():
    if not (line.startswith("vless://") or line.startswith("vmess://")):
        continue
    if is_jp_or_us(line):
        node = gen_clash(line)
        if node:
            nodes.append(node)

# Write mihomo proxy provider
with open("/etc/mihomo/proxy-provider.yaml", "w") as f:
    f.write("proxies:\n")
    yaml.safe_dump(nodes, f, allow_unicode=True, default_flow_style=False)

print(f"Generated {len(nodes)} JP/US nodes in /etc/mihomo/proxy-provider.yaml")
for n in nodes:
    region = "JP" if any(k in n.get("servername","")+n["name"] for k in ["jp","japan","tokyo"]) else "US"
    print(f"  [{region}] {n['name'][:50]}")
