# Copyright (c) Subtrace, Inc.
# SPDX-License-Identifier: BSD-3-Clause

import base64, mimetypes, os, re, subprocess, sys

def exfil_classes():
    exfil = {
        "entrypoints/main/MainImpl.js":      ["Root", "Common", "SDK", "UI", "Logs"],
        "core/sdk/NetworkRequest.js":        ["NetworkRequest"],
        "models/har/HARFormat.js":           ["HARLog", "HAREntry"],
        "models/har/Importer.js":            ["Importer"],
        "models/logs/NetworkLog.js":         ["NetworkLog"],
        "panels/network/NetworkItemView.js": ["NetworkItemView"],
        "panels/network/NetworkLogView.js":  ["NetworkLogView"],
        "panels/network/NetworkPanel.js":    ["NetworkPanel"],
        "ui/legacy/InspectorView.js":        ["InspectorView", "ActionDelegate"],
    }

    for path, arr in exfil.items():
        with open(path) as f:
            src = f.read()
        if "window.subtrace" not in src:
            for obj in arr:
                src = src + "\n\n" + "window.subtrace.{} = {};".format(obj, obj)
            with open(path, "w") as f:
                f.write(src)

def replace_icon_image_url():
    with open("panels/utils/utils.js") as f:
        src = f.read()
    if "url('${url}')" in src:
        with open("panels/utils/utils.js", "w") as f:
            f.write(src.replace("url('${url}')", "var(--image-file-${iconData.iconName})"))

def embed_images(js):
    pos = 0
    pat = re.compile('[\'\"][^\'\"]+\.(avif|bmp|gif|ico|jpg|jpeg|png|svg)[\'\"]')
    while pos < len(js):
        m = pat.search(js, pos)
        if m is None:
            break

        idx = m.start()
        cur = m[0]
        pos = idx + len(cur)

        name = cur.replace('"', "").replace("'", "")
        base = os.path.basename(name)
        path = "Images/{}".format(base)
        if not os.path.exists(path):
            continue

        _, ext = os.path.splitext(base)
        if ext not in mimetypes.types_map:
            continue
        mime = mimetypes.types_map[ext]

        with open(path, "rb") as f:
            img = bytes(f.read())
        repl = cur[:1] + "data:{};base64,{}".format(mime, base64.b64encode(img).decode()) + cur[-1:]

        js = js[:idx] + repl + js[idx+len(cur):]
        pos += len(repl) - len(cur)

    return js

def embed_locale(js):
    with open("core/i18n/locales/en-US.json", "rb") as f:
        return js.replace("./locales/@LOCALE@.json", "data:application/json;base64,{}".format(base64.b64encode(f.read()).decode()))

def embed_live_metrics(js):
    with open("models/live-metrics/web-vitals-injected/web-vitals-injected.generated.js", "rb") as f:
        return js.replace("./web-vitals-injected/web-vitals-injected.generated.js", "data:application/javascript;base64,{}".format(base64.b64encode(f.read()).decode()))

def main():
    os.chdir("devtools-frontend/out/Default/gen/front_end")

    exfil_classes()
    replace_icon_image_url()

    process = subprocess.Popen([
        "esbuild",
        "entrypoints/devtools_app/devtools_app.js",
        "--bundle",
        "--minify",
        "--format=esm",
        "--external:puppeteer",
        "--external:lighthouse",
        "--legal-comments=none",
        "--log-level=error",
    ], stdout=subprocess.PIPE)

    js = process.stdout.read().decode()
    js = embed_images(js)
    js = embed_locale(js)
    js = embed_live_metrics(js)
    sys.stdout.write(js)

if __name__ == "__main__":
    main()
