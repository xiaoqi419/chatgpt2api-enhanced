#!/usr/bin/env python3
""" Registration helper using curl_cffi (two-phase) """
import sys, json, uuid, base64, hashlib, secrets, time
from urllib.parse import parse_qs, urlencode, urlparse
from curl_cffi import requests

AUTH = "https://auth.openai.com"
PLATFORM = "https://platform.openai.com"
CID = "app_2SKx67EdpoN0G6j64rFvigXD"
REDIR = f"{PLATFORM}/auth/callback"
AUD = "https://api.openai.com/v1"
A0C = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

def gen_pkce():
    v = base64.urlsafe_b64encode(secrets.token_bytes(64)).rstrip(b"=").decode()
    c = base64.urlsafe_b64encode(hashlib.sha256(v.encode()).digest()).rstrip(b"=").decode()
    return v, c

def phase1(email, password, proxy=""):
    """Authorize + register + send OTP. Returns {device_id, code_verifier, state, nonce} or error."""
    kwargs = {"impersonate": "chrome", "proxy": proxy} if proxy else {"impersonate": "chrome"}
    s = requests.Session(**kwargs)
    did = uuid.uuid4().hex
    s.cookies.set("oai-did", did, domain=".auth.openai.com")
    cv, cc = gen_pkce()
    st = secrets.token_urlsafe(32)
    n = secrets.token_urlsafe(32)

    params = urlencode({"issuer": AUTH, "client_id": CID, "audience": AUD,
        "redirect_uri": REDIR, "screen_hint": "login_or_signup", "device_id": did,
        "login_hint": email, "scope": "openid profile email offline_access",
        "response_type": "code", "response_mode": "query", "state": st, "nonce": n,
        "code_challenge": cc, "code_challenge_method": "S256", "auth0Client": A0C})
    hdrs = {"Accept": "application/json", "User-Agent": UA, "oai-device-id": did,
        "Accept-Language": "en-US,en;q=0.9"}

    r = s.get(f"{AUTH}/api/accounts/authorize?{params}", headers=hdrs, timeout=30)
    if r.status_code != 200:
        body = r.text[:200]
        if "unsupported_country" in body:
            return {"error": "unsupported_country"}
        return {"error": f"authorize: HTTP {r.status_code} {body}"}

    r = s.post(f"{AUTH}/api/accounts/user/register", json={"username": email, "password": password},
        headers={"Content-Type": "application/json", "User-Agent": UA, "oai-device-id": did,
            "Accept": "application/json", "Accept-Language": "en-US,en;q=0.9"}, timeout=30)
    if r.status_code != 200:
        return {"error": f"register: HTTP {r.status_code}"}

    r = s.get(f"{AUTH}/api/accounts/email-otp/send", headers={"User-Agent": UA, "Referer": f"{AUTH}/create-account/password",
        "Accept": "application/json", "oai-device-id": did}, timeout=30, allow_redirects=True)
    if r.status_code not in (200, 302):
        return {"error": f"send_otp: HTTP {r.status_code}"}

    return {"device_id": did, "code_verifier": cv, "state": st, "nonce": n}
    
def phase2(device_id, code_verifier, email, password, first, last, birthdate, otp_code, proxy=""):
    """Validate OTP + create account + exchange tokens. Returns tokens or error."""
    kwargs = {"impersonate": "chrome", "proxy": proxy} if proxy else {"impersonate": "chrome"}
    s = requests.Session(**kwargs)

    r = s.post(f"{AUTH}/api/accounts/email-otp/validate", json={"code": otp_code},
        headers={"Content-Type": "application/json", "User-Agent": UA, "oai-device-id": device_id,
            "Accept": "application/json", "Referer": f"{AUTH}/email-verification"}, timeout=30)
    if r.status_code != 200:
        return {"error": f"otp: HTTP {r.status_code}"}

    r = s.post(f"{AUTH}/api/accounts/create_account", json={"name": f"{first} {last}", "birthdate": birthdate},
        headers={"Content-Type": "application/json", "User-Agent": UA, "oai-device-id": device_id,
            "Accept": "application/json"}, timeout=30)
    if r.status_code not in (200, 302):
        return {"error": f"create: HTTP {r.status_code}"}

    data = {}
    try:
        if r.text.strip().startswith("{"):
            data = r.json()
    except: pass
    continue_url = data.get("continue_url", "")
    auth_code = ""
    if continue_url and "code=" in continue_url:
        auth_code = parse_qs(urlparse(continue_url).query).get("code", [""])[0]
    if not auth_code:
        return {"error": "no auth code in continue_url"}

    r = s.post(f"{AUTH}/api/accounts/oauth/token", json={"client_id": CID,
        "code_verifier": code_verifier, "grant_type": "authorization_code", "code": auth_code,
        "redirect_uri": REDIR},
        headers={"Accept": "*/*", "User-Agent": UA, "Content-Type": "application/json",
            "auth0-client": A0C, "Origin": PLATFORM, "Referer": f"{PLATFORM}/"}, timeout=60)
    if r.status_code != 200:
        return {"error": f"token: HTTP {r.status_code}"}

    tokens = r.json()
    return {
        "access_token": tokens.get("access_token", ""),
        "refresh_token": tokens.get("refresh_token", ""),
        "id_token": tokens.get("id_token", ""),
    }

if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    if cmd == "phase1":
        result = phase1(sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv) > 4 else "")
        print(json.dumps(result))
    elif cmd == "phase2":
        result = phase2(*sys.argv[2:8], sys.argv[8] if len(sys.argv) > 8 else "")
        print(json.dumps(result))
    else:
        print(json.dumps({"error": "usage: phase1|phase2 ..."}))
