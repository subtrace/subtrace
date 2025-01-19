__checked__ = False
if not __checked__:
    import os
    __checked__ = True
    if os.uname().sysname == "Linux":
        if "SUBTRACE_RUN" in os.environ and os.environ["SUBTRACE_RUN"] == "1":
            import truststore
            truststore.inject_into_ssl()
        else:
            def reexec(binary):
                import sys, ssl
                environ = os.environ
                paths = ssl.get_default_verify_paths()
                if paths.cafile and "REQUESTS_CA_BUNDLE" not in environ:
                    environ["REQUESTS_CA_BUNDLE"] = paths.cafile
                os.execve(os.path.join(os.path.dirname(os.path.realpath(__file__)), binary), ["subtrace", "run", "--"] + [sys.executable] + sys.argv, environ)

            if os.uname().machine == "x86_64":
                reexec("subtrace-linux-amd64")
            elif os.uname().machine == "aarch64":
                reexec("subtrace-linux-arm64")
