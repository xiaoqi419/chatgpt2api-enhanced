import base64, re
from urllib.request import urlopen, Request

API_URL = "https://liangxin.xyz/api/v1/liangxin?OwO=32e0b21438d4b570e41a9c9a7ef4aa5d"

def fetch():
    req = Request(API_URL, headers={"User-Agent": "curl/8"})
    raw = urlopen(req, timeout=20).read().decode()
    try:
        decoded = base64.b64decode(raw).decode()
    except:
        decoded = raw

    proxies = []
    for line in decoded.strip().split():
        if not (line.startswith("vless://") or line.startswith("vmess://")):
            continue

        sni = ""
        name = ""
        if "sni=" in line:
            try:
                sni = line.split("sni=")[1].split("&")[0].lower()
            except:
                pass
        if "host=" in line and "sni=" not in line:
            try:
                sni = line.split("host=")[1].split("&")[0].lower()
            except:
                pass
        if "#" in line:
            try:
                name = line.split("#")[1]
                name = re.sub(r'%([0-9A-Fa-f]{2})', lambda m: chr(int(m.group(1), 16)), name)
            except:
                pass

        name_lower = name.lower()

        # Match JP
        jp_keywords = ["jp", "japan", "tokyo", "osaka", "\U0001f1ef\U0001f1f5",
                       "日本", "东京"]
        is_jp = any(k in sni or k in name_lower for k in jp_keywords)

        # Match US
        us_keywords = ["us", "usa", "united", "america", "\U0001f1fa\U0001f1f8",
                       "los angeles", "new york", "seattle", "dallas", "miami",
                       "san jose", "chicago", "美国", "洛杉矶", "纽约"]
        is_us = any(k in sni or k in name_lower for k in us_keywords)

        if is_jp or is_us:
            proxies.append(line)

    return proxies

if __name__ == "__main__":
    proxies = fetch()
    print(f"Found {len(proxies)} JP/US proxies")
    with open("/tmp/jpus_proxies.txt", "w") as f:
        f.write("\n".join(proxies))

    for p in proxies[:5]:
        if "#" in p:
            name = p.split("#")[1]
            try:
                name = re.sub(r'%([0-9A-Fa-f]{2})', lambda m: chr(int(m.group(1), 16)), name)
            except:
                pass
            print(f"  {name[:80]}")
