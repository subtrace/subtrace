import os
import requests, urllib
import subtrace

print(requests.get("https://httpbin.org/uuid").text.strip())
print(urllib.request.urlopen("https://httpbin.org/uuid").read().decode("utf-8").strip())
