#!/usr/bin/env python
import subprocess

from setuptools import setup

setup(
    name="subtrace",
    description="Subtrace helper for Python apps",
    version="0.{}".format(len(subprocess.check_output(["git", "log", "--oneline"]).decode("utf-8").split("\n"))),
    url="https://subtrace.dev",
    keywords="subtrace tracing api observability devtools",
    author="Subtrace, Inc.",
    author_email="support@subtrace.dev",
    license="BSD",
    python_requires=">=3.9",
    packages=["subtrace"],
    package_dir={"subtrace": "subtrace"},
    package_data={"subtrace": ["subtrace-linux-amd64", "subtrace-linux-arm64"]},
    include_package_data=True,
    zip_safe=False,

    install_requires=[
        "truststore",
    ],

    classifiers=[
        "License :: OSI Approved :: BSD License",
        "Programming Language :: Python",
        "Programming Language :: Python :: 2",
        "Programming Language :: Python :: 3",
    ],

    project_urls={
        "Docs": "https://docs.subtrace.dev",
        "Github": "https://github.com/subtrace/subtrace",
    },
)
