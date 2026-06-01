#!/usr/bin/env python3
"""Test registration connection through proxy"""
import sys, json
from curl_cffi import requests

s = requests.Session(impersonate="chrome", proxy="http://127.0.0.1:7890")
r = s.get("https://auth.openai.com/api/accounts/authorize", timeout=20)
print(f"Status: {r.status_code}")
if r.status_code == 200:
    print("OK - Registration endpoint reachable through proxy")
else:
    print(f"Response: {r.text[:200]}")
