#!/usr/bin/env node

const child_process = require("child_process");
const path = require("path");
const fs = require("fs");

function main() {
  if (process.platform !== "linux") {
    console.error(`unsupported platform: ${process.platform}`);
    return 1;
  }

  let arch;
  switch (process.arch) {
    case "x64":
      arch = "amd64";
      break;
    case "arm64":
      arch = "arm64";
      break;
    default:
      console.error(`unsupported arch: ${process.arch}`);
      return 1;
  }

  const bin = path.join(__dirname, `subtrace-linux-${arch}`);
  if (!fs.existsSync(bin)) {
    console.error(`binary ${bin} not found`);
    return 1;
  }

  const args = ["run", "--", ...process.argv.slice(2)];
  const child = child_process.spawnSync(bin, args, { stdio: "inherit" });
  if (child.error) {
    console.error(child.error.message);
    return 1;
  }

  return child.status !== null ? child.status : 1;
}

process.exit(main());
